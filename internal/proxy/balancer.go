package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

const minStatusCodes = 50

// StatusEntry records a single request outcome with timestamp for display.
type StatusEntry struct {
	Code      int   `json:"code"`
	LatencyMs int64 `json:"latency_ms"`
	Timestamp int64 `json:"timestamp"` // unix timestamp in seconds
}

// Backend health states. The backend moves between them based on classified
// request outcomes, a 60s reconcile pass, and external health probes.
//
// Routable states (participate in selection): Healthy, Degraded, Probing.
// Non-routable states (effectiveWeight==0): Isolated, Quarantine.
const (
	stateHealthy    int32 = 0 // routable, full weight
	stateDegraded   int32 = 1 // routable, reduced weight (soft health factor)
	stateDisabled   int32 = 2 // retained for back-compat mapping; superseded by isolated/quarantine
	stateIsolated   int32 = 3 // NOT routable, TTL cooldown (429/403/5xx/transport)
	stateProbing    int32 = 4 // routable, weight = 1 (half-open trial)
	stateQuarantine int32 = 5 // NOT routable, long TTL + alert (401 / consec>=5)
)

// Thresholds for state transitions driven by consecutive request outcomes.
const (
	consecErrToDegrade    = 3 // consecutive errors: healthy -> degraded
	consecErrToQuarantine = 5 // consecutive errors: -> quarantine
	consecOKToHealthy     = 3 // consecutive successes: degraded -> healthy (probe-driven)
)

// TTL policy parameters (see research.md R4 / §11). All durations in seconds.
const (
	ttlBase429 = 30      // 429 base TTL
	ttlCap429  = 5 * 60  // 429 cap
	ttlBase403 = 2 * 60  // 403 base TTL
	ttlCap403  = 30 * 60 // 403 cap
	ttlBase5xx = 30      // 5xx / transport base TTL
	ttlCap5xx  = 5 * 60  // 5xx / transport cap
	ttlBase401 = 15 * 60 // 401 -> Quarantine base TTL
	ttlCap401  = 30 * 60 // 401 cap
)

// Reconcile / recovery windows.
const (
	cleanWindowSecs        = 5 * 60  // Degraded->Healthy clean window; consec decay window
	idleProbeSecs          = 10 * 60 // Healthy idle beyond this is marked due-for-probe
	failsafeShieldSecs     = 60      // failsafe-promoted node protected from re-isolation
	failsafeHysteresisSecs = 30      // failsafe won't re-run within this window
	reconcilePeriod        = 60 * time.Second
)

// Failsafe thresholds.
const (
	failsafeTrigger   = 3 // routable < this triggers the failsafe
	failsafeTargetCap = 7 // promote until routable >= min(this, total)
)

// Health / soft weighting factors.
const (
	degradedHealthFactor = 0.3  // Degraded weight multiplier (0.2-0.5 range, default 0.3)
	probingWeight        = 1    // Probing backends participate at fixed weight 1
	budgetFloor          = 0.05 // budget factor never drops below this (never 0)
	latencyFloor         = 0.3  // latency factor floor
	latencyKneeMs        = 8000 // latency beyond this starts reducing weight
)

// ErrorClass categorizes a request outcome for health accounting.
type ErrorClass int

const (
	ErrNone      ErrorClass = iota // 2xx success
	ErrClient                      // 400/404/422 & other 4xx: caller's fault, passthrough, ignored
	ErrAuth                        // 401 only: backend key invalid -> Quarantine + alert
	ErrForbidden                   // 403 only: -> Isolated (2min base), quota-body may extend to daily reset
	ErrRateLimit                   // 429: transient -> Isolated (30s base), honor Retry-After
	ErrServer                      // 5xx: upstream fault -> Isolated (30s base)
	ErrTransport                   // connection-level failure -> Isolated (30s base)
	ErrCanceled                    // client aborted the request: not the backend's fault, ignored
)

// IsClientCanceled reports whether err is the result of the *client* aborting
// the request (context canceled / deadline propagated from the inbound request),
// as opposed to a genuine upstream transport failure. These are not the
// backend's fault and must not count against its health.
func IsClientCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// http.Client wraps the context error in a *url.Error whose message ends in
	// "context canceled"; errors.Is above catches the common case, this is a
	// defensive fallback for wrapped strings that lose the sentinel.
	return strings.Contains(err.Error(), "context canceled")
}

// ClassifyError maps an HTTP status code and/or transport error to an ErrorClass.
// A non-nil transportErr always takes precedence (no usable status code).
//
//	401       -> ErrAuth      (Quarantine + alert)
//	403       -> ErrForbidden (Isolated, 2min base; separate from 401)
//	429       -> ErrRateLimit (Isolated, 30s base, honor Retry-After)
//	5xx       -> ErrServer    (Isolated, 30s base)
//	other 4xx -> ErrClient    (passthrough, no state change)
func ClassifyError(statusCode int, transportErr error) ErrorClass {
	if transportErr != nil {
		if IsClientCanceled(transportErr) {
			return ErrCanceled
		}
		return ErrTransport
	}
	switch {
	case statusCode >= 200 && statusCode < 300:
		return ErrNone
	case statusCode == 401:
		return ErrAuth
	case statusCode == 403:
		return ErrForbidden
	case statusCode == 429:
		return ErrRateLimit
	case statusCode >= 500:
		return ErrServer
	case statusCode >= 400:
		return ErrClient
	default:
		// 1xx/3xx: treat as success for health purposes.
		return ErrNone
	}
}

// countsAgainstHealth reports whether a class should affect the health state.
func (c ErrorClass) countsAgainstHealth() bool {
	switch c {
	case ErrRateLimit, ErrServer, ErrTransport, ErrForbidden:
		return true
	default:
		return false
	}
}

// httpCodeForClass returns a representative HTTP code for a class, used when the
// caller drives RecordResult without a stored lastHTTPCode (tests, transport).
func httpCodeForClass(c ErrorClass) int {
	switch c {
	case ErrAuth:
		return 401
	case ErrForbidden:
		return 403
	case ErrRateLimit:
		return 429
	case ErrServer:
		return 500
	default:
		return 0
	}
}

// Backend represents a single upstream API endpoint with its HTTP client.
type Backend struct {
	Name             string
	URL              string
	APIKey           string
	Weight           int
	client           *http.Client
	state            atomic.Int32  // stateHealthy/Degraded/Isolated/Probing/Quarantine
	enteredAt        atomic.Int64  // unix s: when the current state was entered
	ttlUntil         atomic.Int64  // unix s: cooldown expiry for Isolated/Quarantine; 0 = none
	retryAfter       atomic.Int64  // unix s: absolute expiry parsed from a 429 Retry-After; 0 = none
	lastHTTPCode     atomic.Int64  // most recent response code
	lastCodeAt       atomic.Int64  // unix s: when lastHTTPCode was recorded (probe-trial freshness)
	consecErr        atomic.Int64  // consecutive health-impacting errors
	consecOK         atomic.Int64  // consecutive successes (for degraded->healthy)
	lastErr          atomic.Int64  // unix timestamp of last error
	lastSuccessAt    atomic.Int64  // unix timestamp of last 2xx
	lastLatencyMs    atomic.Int64  // latency of the most recent request in ms
	probeShieldUntil atomic.Int64  // unix s: failsafe-promoted node protected from re-isolation
	currentWeight    int           // SWRR running cursor (guarded by LoadBalancer.selMu)
	validationFailed atomic.Bool   // set on startup validation failure; never auto-recovered
	quotaExhausted   atomic.Bool   // external hard quota flag (kept; no longer the primary gate)
	quotaLimit       atomic.Int64  // hard_limit_usd * 100 (cents precision)
	quotaUsage       atomic.Int64  // total_usage * 100 (cents precision)
	quotaCheckedAt   atomic.Int64  // unix timestamp of last quota check
	statusEntries    []StatusEntry // ring buffer of recent requests (code + latency + timestamp)
	statusCodeDist   map[int]int   // distribution of status codes within the ring buffer
	statusCapacity   int           // dynamic capacity: max(50, 10 * backends_count)
	statusMu         sync.Mutex
	owner            *LoadBalancer // balancer that owns this backend (for clock/jitter access)
}

// defaultLB provides clock and jitter for backends constructed without an owner
// (defensive; NewLoadBalancer always sets owner). It holds no backends.
var defaultLB = &LoadBalancer{nowFn: time.Now, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}

// lbOrDefault returns the owning balancer, or the package default if unset.
func (b *Backend) lbOrDefault() *LoadBalancer {
	if b.owner != nil {
		return b.owner
	}
	return defaultLB
}

// Client returns the backend's dedicated HTTP client.
func (b *Backend) Client() *http.Client { return b.client }

// State returns the current health state.
func (b *Backend) State() int32 { return b.state.Load() }

// TTLUntil returns the cooldown expiry (unix seconds; 0 = none).
func (b *Backend) TTLUntil() int64 { return b.ttlUntil.Load() }

// stateName converts a numeric state to its string label. New states extend the
// enum; "disabled" is retained for any legacy value still mapped to it.
func stateName(s int32) string {
	switch s {
	case stateDegraded:
		return "degraded"
	case stateDisabled:
		return "disabled"
	case stateIsolated:
		return "isolated"
	case stateProbing:
		return "probing"
	case stateQuarantine:
		return "quarantine"
	default:
		return "healthy"
	}
}

// isRoutable reports whether a state participates in selection.
func isRoutable(s int32) bool {
	switch s {
	case stateHealthy, stateDegraded, stateProbing:
		return true
	default:
		return false
	}
}

// alert raises a Quarantine alert. v1 delivery is a structured warning log
// (FR-038); the channel can be upgraded later without changing call sites.
func alert(name, reason string) {
	log.Printf("[backend:%s] ALERT quarantine: %s", name, reason)
}

// ---------------------------------------------------------------------------
// TTL policy
// ---------------------------------------------------------------------------

// baseCapForCode returns the base and cap TTL (seconds) for a status code.
func baseCapForCode(code int) (base, cap int64) {
	switch {
	case code == 429:
		return ttlBase429, ttlCap429
	case code == 403:
		return ttlBase403, ttlCap403
	case code == 401:
		return ttlBase401, ttlCap401
	case code >= 500:
		return ttlBase5xx, ttlCap5xx
	default:
		// transport / unknown: treat as server-class.
		return ttlBase5xx, ttlCap5xx
	}
}

// secondsUntilCSTMidnight returns seconds from now until the next 00:00 CST,
// reusing the fixed zone used by quotaResetLoop.
func secondsUntilCSTMidnight(now time.Time) int64 {
	cst := time.FixedZone("CST", 8*3600)
	n := now.In(cst)
	next := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, cst)
	return int64(next.Sub(n).Seconds())
}

// jitter applies ±20% to a seconds value using the injected rand source.
func (lb *LoadBalancer) jitter(secs int64) int64 {
	if secs <= 0 {
		return secs
	}
	lb.randMu.Lock()
	f := lb.rng.Float64() // [0,1)
	lb.randMu.Unlock()
	return int64(float64(secs) * (0.8 + 0.4*f))
}

// computeTTL returns the cooldown duration in seconds for a backend that just
// failed with the given code, honoring Retry-After and the 403-quota→daily-reset
// special case, with ±20% jitter applied. consec drives exponential backoff via
// the reconcile probe path (handled in judgeProbing); the initial entry uses the
// base TTL for the code.
//   - retryAfterUnix > 0: use the absolute expiry from the 429 Retry-After header.
//   - quotaBody: a 403 whose body clearly indicates quota → TTL to next 00:00 CST.
func (lb *LoadBalancer) computeTTL(code int, retryAfterUnix int64, quotaBody bool, now time.Time) int64 {
	nowUnix := now.Unix()
	// 429 Retry-After takes precedence: honor the absolute expiry directly (no jitter).
	if code == 429 && retryAfterUnix > nowUnix {
		return retryAfterUnix - nowUnix
	}
	// 403 quota body → lock until the daily reset (no jitter; it's an absolute boundary).
	if code == 403 && quotaBody {
		return secondsUntilCSTMidnight(now)
	}
	base, _ := baseCapForCode(code)
	return lb.jitter(base)
}

// RecordResult updates the backend health state based on a classified outcome.
// It is the request-path entry point for exit rules (US1):
//   - ErrNone: reset consecutive errors; drive Degraded->Healthy recovery.
//   - ErrClient/ErrCanceled: ignored entirely (caller's request was bad or aborted) — NO state/counter change.
//   - ErrAuth (401): Quarantine + alert.
//   - ErrRateLimit/ErrServer/ErrTransport/ErrForbidden: Isolated with code-specific TTL, consecErr++.
//   - consecErr reaching the quarantine threshold: Quarantine regardless of class.
//   - A Probing backend shielded by the failsafe (probeShieldUntil) is not re-isolated.
//
// This method requires a *LoadBalancer receiver access for TTL/jitter; it is
// exposed on Backend via lb.recordResult. RecordResult is kept as a Backend
// method for call-site compatibility and delegates through the owning balancer.
func (b *Backend) RecordResult(class ErrorClass) {
	b.lbOrDefault().recordResult(b, class, 0, false)
}

// RecordResultDetailed is the request-path entry point that carries the parsed
// absolute Retry-After (unix seconds; 0 = none) and whether the response body
// indicated a quota failure. These refine the 429/403 TTL (FR-012, FR-013).
func (b *Backend) RecordResultDetailed(class ErrorClass, retryAfterUnix int64, quotaBody bool) {
	b.lbOrDefault().recordResult(b, class, retryAfterUnix, quotaBody)
}

// recordResult is the balancer-aware implementation. retryAfterUnix and quotaBody
// come from the request path (handler) for 429/403 refinement; both default to 0/false.
func (lb *LoadBalancer) recordResult(b *Backend, class ErrorClass, retryAfterUnix int64, quotaBody bool) {
	now := lb.nowFn()
	switch class {
	case ErrClient, ErrCanceled:
		// Request-level 4xx or client abort: not the backend's fault.
		// MUST NOT mutate state, consecErr, or ttlUntil (FR-008).
		return

	case ErrNone:
		b.consecErr.Store(0)
		b.lastSuccessAt.Store(now.Unix())
		// Promote degraded -> healthy after enough consecutive successes.
		if b.state.Load() == stateDegraded {
			if b.consecOK.Add(1) >= consecOKToHealthy {
				b.setState(stateHealthy, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] recovered: degraded -> healthy", b.Name)
			}
		} else {
			b.consecOK.Store(0)
		}
		return

	case ErrAuth:
		// 401: key invalid -> Quarantine + alert.
		b.lastErr.Store(now.Unix())
		b.consecOK.Store(0)
		b.consecErr.Add(1)
		lb.setQuarantine(b, 401, now, "auth failure (401)")
		return

	default: // ErrRateLimit, ErrServer, ErrTransport, ErrForbidden
		b.lastErr.Store(now.Unix())
		b.consecOK.Store(0)
		n := b.consecErr.Add(1)

		// A shielded, freshly failsafe-promoted Probing node is not re-isolated.
		if b.state.Load() == stateProbing && now.Unix() < b.probeShieldUntil.Load() {
			return
		}

		// Consecutive-failure escalation to Quarantine regardless of class.
		if n >= consecErrToQuarantine {
			lb.setQuarantine(b, int(b.lastHTTPCode.Load()), now, "5 consecutive failures")
			return
		}

		code := httpCodeForClass(class)
		if lc := b.lastHTTPCode.Load(); lc != 0 && (class == ErrServer || class == ErrRateLimit || class == ErrForbidden) {
			code = int(lc)
		}
		lb.setIsolated(b, code, retryAfterUnix, quotaBody, now)
	}
}

// setState stores a new state and records the entry timestamp.
func (b *Backend) setState(s int32, now time.Time) {
	b.state.Store(s)
	b.enteredAt.Store(now.Unix())
}

// setIsolated moves a backend to Isolated with a code-specific TTL. No-op alert.
func (lb *LoadBalancer) setIsolated(b *Backend, code int, retryAfterUnix int64, quotaBody bool, now time.Time) {
	ttl := lb.computeTTL(code, retryAfterUnix, quotaBody, now)
	b.ttlUntil.Store(now.Unix() + ttl)
	if b.state.Swap(stateIsolated) != stateIsolated {
		log.Printf("[backend:%s] isolated: code=%d ttl=%ds", b.Name, code, ttl)
	}
	b.enteredAt.Store(now.Unix())
}

// setQuarantine moves a backend to Quarantine with a long TTL and raises an alert
// on entry (FR-038).
func (lb *LoadBalancer) setQuarantine(b *Backend, code int, now time.Time, reason string) {
	base, _ := baseCapForCode(401) // Quarantine always uses the 401 TTL band
	ttl := lb.jitter(base)
	b.ttlUntil.Store(now.Unix() + ttl)
	if b.state.Swap(stateQuarantine) != stateQuarantine {
		alert(b.Name, reason)
	}
	b.enteredAt.Store(now.Unix())
}

// SetIsolated is the request-path entry point that isolates a backend for a
// specific code, passing a parsed absolute Retry-After (unix seconds; 0 = none)
// and whether the body indicated a quota failure. Used by handler.go on 429/403.
func (b *Backend) SetIsolated(code int, retryAfterUnix int64, quotaBody bool) {
	lb := b.lbOrDefault()
	lb.setIsolated(b, code, retryAfterUnix, quotaBody, lb.nowFn())
}

// SetState forces the backend into a specific health state (used by active probes/tests).
func (b *Backend) SetState(s int32) {
	b.state.Store(s)
	b.enteredAt.Store(b.lbOrDefault().nowFn().Unix())
	if s == stateHealthy {
		b.consecErr.Store(0)
		b.consecOK.Store(0)
		b.ttlUntil.Store(0)
	}
}

// ---------------------------------------------------------------------------
// LoadBalancer
// ---------------------------------------------------------------------------

// LoadBalancer selects backends using smooth weighted round-robin (SWRR) over
// effective weights, with a 5-state health machine and a 60s reconcile pass.
type LoadBalancer struct {
	mu       sync.RWMutex
	backends []*Backend

	selMu sync.Mutex // guards SWRR currentWeight cursors

	nowFn func() time.Time // injectable clock (default time.Now)

	randMu sync.Mutex // guards rng
	rng    *rand.Rand // injectable randomness for jitter

	lastFailsafeAt atomic.Int64 // unix s of last failsafe run (hysteresis)
}

// NewLoadBalancer builds backends from config and starts the reconcile + quota tickers.
func NewLoadBalancer(cfgs []config.BackendAPI) *LoadBalancer {
	lb := &LoadBalancer{
		nowFn: time.Now,
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		lb.backends = append(lb.backends, lb.newBackend(c))
	}
	lb.updateCapacities()
	go lb.quotaResetLoop()
	go lb.reconcileLoop()
	return lb
}

// newBackend constructs a Backend with its dedicated HTTP client and owner back-ref.
func (lb *LoadBalancer) newBackend(c config.BackendAPI) *Backend {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // prevent gzip on SSE streams (e.g. MiniMax returns compressed binary)
	}
	return &Backend{
		Name:   c.Name,
		URL:    c.URL,
		APIKey: c.APIKey,
		Weight: c.Weight,
		client: &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second, // long for streaming
		},
		owner: lb,
	}
}

// updateCapacities sets each backend's ring-buffer capacity to max(minStatusCodes, 10*backends_count).
func (lb *LoadBalancer) updateCapacities() {
	count := len(lb.backends)
	if count == 0 {
		count = 1
	}
	cap := 10 * count
	if cap < minStatusCodes {
		cap = minStatusCodes
	}
	for _, b := range lb.backends {
		b.statusMu.Lock()
		b.statusCapacity = cap
		b.statusMu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Weighting (US4): base × healthFactor × budgetFactor × latencyFactor
// ---------------------------------------------------------------------------

// budgetFactor maps spend ratio (usage/limit) to a soft weight multiplier.
// It NEVER returns 0 (floor 0.05) and NEVER changes state (FR-009, FR-028):
//
//	<80%   -> 1.0
//	80-95% -> linear 1.0 down to 0.3
//	95-99% -> linear 0.3 down to 0.05
//	>=99%  -> 0.05
func budgetFactor(usageCents, limitCents int64) float64 {
	if limitCents <= 0 {
		return 1.0 // no limit configured: no budget pressure
	}
	ratio := float64(usageCents) / float64(limitCents)
	switch {
	case ratio < 0.80:
		return 1.0
	case ratio < 0.95:
		// 0.80->1.0, 0.95->0.3
		return 1.0 - (ratio-0.80)/(0.95-0.80)*(1.0-0.3)
	case ratio < 0.99:
		// 0.95->0.3, 0.99->0.05
		return 0.3 - (ratio-0.95)/(0.99-0.95)*(0.3-0.05)
	default:
		return budgetFloor
	}
}

// latencyFactor maps recent latency to a soft weight multiplier in [0.3, 1.0]:
//
//	<=8s -> 1.0; else 8000/latencyMs, floored at 0.3 (FR-029).
func latencyFactor(latencyMs int64) float64 {
	if latencyMs <= latencyKneeMs {
		return 1.0
	}
	f := float64(latencyKneeMs) / float64(latencyMs)
	if f < latencyFloor {
		return latencyFloor
	}
	return f
}

// healthFactor returns the state-based weight multiplier. Probing/Isolated/
// Quarantine are handled directly in effectiveWeight (fixed 1 / 0), so this
// only distinguishes Healthy vs Degraded.
func healthFactor(state int32) float64 {
	switch state {
	case stateDegraded:
		return degradedHealthFactor
	default:
		return 1.0
	}
}

// effectiveWeight returns the selection weight after applying health, budget,
// and latency factors. Returns 0 when the backend must not be selected.
//   - validationFailed / Isolated / Quarantine / Disabled -> 0
//   - Probing -> fixed probingWeight (bypasses soft factors)
//   - Healthy/Degraded -> base × healthFactor × budgetFactor × latencyFactor (min 1 if routable)
func (b *Backend) effectiveWeight() int {
	// Hard gates: never selectable regardless of state.
	//   - validationFailed: startup validation failed; never auto-recovers.
	//   - quotaExhausted: an upstream "insufficient balance"/suspended-account
	//     signal (MarkQuotaExhausted) or an external hard-quota probe. This is a
	//     real account-level block, distinct from the SOFT budgetFactor that only
	//     biases weight as estimated spend approaches the configured daily limit.
	if b.validationFailed.Load() || b.quotaExhausted.Load() {
		return 0
	}
	state := b.state.Load()
	switch state {
	case stateProbing:
		return probingWeight
	case stateIsolated, stateQuarantine, stateDisabled:
		return 0
	}
	// Healthy or Degraded: apply soft factors.
	bf := budgetFactor(b.quotaUsage.Load(), b.quotaLimit.Load())
	lf := latencyFactor(b.lastLatencyMs.Load())
	w := float64(b.Weight) * healthFactor(state) * bf * lf
	iw := int(w)
	if iw < 1 {
		iw = 1 // keep a sliver of traffic so a routable node is never starved to 0
	}
	return iw
}

// Pick selects a routable backend using smooth weighted round-robin (SWRR).
// Returns nil if no routable backend is available. O(n), no per-call allocation.
func (lb *LoadBalancer) Pick() *Backend {
	return lb.pick(nil)
}

// PickExcluding is like Pick but skips any backend whose name is in exclude.
func (lb *LoadBalancer) PickExcluding(exclude map[string]bool) *Backend {
	return lb.pick(exclude)
}

// pick implements SWRR selection. Classic Nginx algorithm:
//
//	each round: currentWeight += effectiveWeight; total += effectiveWeight;
//	pick the backend with the max currentWeight; selected.currentWeight -= total.
func (lb *LoadBalancer) pick(exclude map[string]bool) *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	lb.selMu.Lock()
	defer lb.selMu.Unlock()

	var best *Backend
	total := 0
	for _, b := range lb.backends {
		if exclude != nil && exclude[b.Name] {
			continue
		}
		w := b.effectiveWeight()
		if w <= 0 {
			continue
		}
		total += w
		b.currentWeight += w
		if best == nil || b.currentWeight > best.currentWeight {
			best = b
		}
	}
	if best == nil {
		return nil
	}
	best.currentWeight -= total
	return best
}

// ---------------------------------------------------------------------------
// Ring buffer recording + recent-code window
// ---------------------------------------------------------------------------

// RecordRequest records a request outcome (status code + latency) in the ring
// buffer. Also updates lastHTTPCode, lastLatencyMs, and (on 2xx) lastSuccessAt.
func (b *Backend) RecordRequest(code int, latencyMs int64) {
	nowUnix := b.lbOrDefault().nowFn().Unix()
	b.lastLatencyMs.Store(latencyMs)
	b.lastHTTPCode.Store(int64(code))
	b.lastCodeAt.Store(nowUnix)
	if code >= 200 && code < 300 {
		b.lastSuccessAt.Store(nowUnix)
	}
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	cap := b.statusCapacity
	if cap < minStatusCodes {
		cap = minStatusCodes
	}
	if b.statusCodeDist == nil {
		b.statusCodeDist = make(map[int]int)
	}
	b.statusEntries = append(b.statusEntries, StatusEntry{
		Code:      code,
		LatencyMs: latencyMs,
		Timestamp: b.lbOrDefault().nowFn().Unix(),
	})
	if len(b.statusEntries) > cap {
		oldCode := b.statusEntries[0].Code
		if b.statusCodeDist[oldCode] > 0 {
			b.statusCodeDist[oldCode]--
			if b.statusCodeDist[oldCode] == 0 {
				delete(b.statusCodeDist, oldCode)
			}
		}
		b.statusEntries = b.statusEntries[1:]
	}
	b.statusCodeDist[code]++
}

// RecordStatusCode is kept for call sites that don't yet have latency available.
func (b *Backend) RecordStatusCode(code int) {
	b.RecordRequest(code, 0)
}

// recentCodes returns the codes from the last ~20 entries or within the last
// ~5 minutes (whichever is tighter), reading under statusMu (R2, FR-037).
func (b *Backend) recentCodes(now time.Time) []int {
	const maxEntries = 20
	cutoff := now.Add(-5 * time.Minute).Unix()
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	n := len(b.statusEntries)
	if n == 0 {
		return nil
	}
	start := 0
	if n > maxEntries {
		start = n - maxEntries
	}
	out := make([]int, 0, n-start)
	for i := start; i < n; i++ {
		if b.statusEntries[i].Timestamp >= cutoff {
			out = append(out, b.statusEntries[i].Code)
		}
	}
	return out
}

// latestRecentSuccess reports whether the most recent recorded code was a 2xx.
// Used by judgeProbing to decide a trial's outcome.
func (b *Backend) latestRecentSuccess() (ok bool, has bool) {
	c := b.lastHTTPCode.Load()
	if c == 0 {
		return false, false
	}
	return c >= 200 && c < 300, true
}

// GetStatusEntries returns a copy of the ring buffer (code + latency + timestamp).
func (b *Backend) GetStatusEntries() []StatusEntry {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	result := make([]StatusEntry, len(b.statusEntries))
	copy(result, b.statusEntries)
	return result
}

// GetRecentLatencyMs returns the latency of the most recent request.
func (b *Backend) GetRecentLatencyMs() int64 {
	return b.lastLatencyMs.Load()
}

// GetStatusCodeDist returns the distribution of status codes within the ring buffer.
func (b *Backend) GetStatusCodeDist() map[int]int {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	result := make(map[int]int)
	for k, v := range b.statusCodeDist {
		result[k] = v
	}
	return result
}

// GetErrorRate returns the non-2xx error rate from the ring buffer.
func (b *Backend) GetErrorRate() float64 {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	if len(b.statusEntries) == 0 {
		return 0
	}
	non2xx := 0
	for _, e := range b.statusEntries {
		if e.Code < 200 || e.Code >= 300 {
			non2xx++
		}
	}
	return float64(non2xx) / float64(len(b.statusEntries)) * 100
}

// ---------------------------------------------------------------------------
// Reconcile pass (US2/US3/US5)
// ---------------------------------------------------------------------------

// reconcileLoop runs the convergence pass every reconcilePeriod. It mirrors the
// quotaResetLoop goroutine pattern and is started from NewLoadBalancer.
func (lb *LoadBalancer) reconcileLoop() {
	ticker := time.NewTicker(reconcilePeriod)
	defer ticker.Stop()
	for range ticker.C {
		lb.Reconcile(lb.nowFn())
	}
}

// Reconcile runs one convergence pass in the documented order (FR-032, FR-033):
//  1. releaseExpired  — Isolated/Quarantine past TTL -> Probing
//  2. judgeProbing    — Probing trial result -> Degraded (ok) or Isolated×2 (fail)
//  3. softUpDown      — Degraded->Healthy after clean window; consec decay; idle mark
//  4. refreshWeights  — (no cached factors; weights are computed on demand)
//  5. failsafe        — ensure minimum routable count
//
// It is a pure function of current state + the injected clock; safe to call from
// the ticker and from tests.
func (lb *LoadBalancer) Reconcile(now time.Time) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	lb.releaseExpired(now)
	lb.judgeProbing(now)
	lb.softUpDown(now)
	lb.refreshWeights(now)
	lb.failsafe(now)
}

// releaseExpired moves Isolated/Quarantine backends whose TTL has elapsed into
// half-open Probing (weight 1). Jitter was already applied when the TTL was set.
func (lb *LoadBalancer) releaseExpired(now time.Time) {
	nowUnix := now.Unix()
	for _, b := range lb.backends {
		s := b.state.Load()
		if s != stateIsolated && s != stateQuarantine {
			continue
		}
		until := b.ttlUntil.Load()
		if until > 0 && nowUnix > until {
			b.setState(stateProbing, now)
			b.ttlUntil.Store(0)
			log.Printf("[backend:%s] TTL expired: %s -> probing", b.Name, stateName(s))
		}
	}
}

// judgeProbing evaluates half-open Probing backends by their latest recent
// result: success -> Degraded (reset consecErr); failure -> Isolated with
// ttl = min(prevTTL×2, cap). Shielded (failsafe) probes are given grace.
func (lb *LoadBalancer) judgeProbing(now time.Time) {
	nowUnix := now.Unix()
	for _, b := range lb.backends {
		if b.state.Load() != stateProbing {
			continue
		}
		// Respect the failsafe shield: don't judge until the shield expires.
		if sh := b.probeShieldUntil.Load(); sh > 0 && nowUnix < sh {
			continue
		}
		// Only judge once a real trial result has arrived AFTER entering Probing.
		// Otherwise the stale lastHTTPCode from the failure that isolated the
		// backend would immediately re-isolate it before any request is tried.
		if b.lastCodeAt.Load() < b.enteredAt.Load() {
			continue // no post-promotion trial yet; wait for the next tick
		}
		ok, has := b.latestRecentSuccess()
		if !has {
			continue // no trial result yet; wait for the next tick
		}
		if ok {
			b.setState(stateDegraded, now)
			b.consecErr.Store(0)
			b.consecOK.Store(0)
			log.Printf("[backend:%s] probe trial ok: probing -> degraded", b.Name)
		} else {
			code := int(b.lastHTTPCode.Load())
			_, cap := baseCapForCode(code)
			prev := b.ttlUntil.Load() - b.enteredAt.Load()
			if prev <= 0 {
				prev, _ = baseCapForCode(code)
			}
			ttl := prev * 2
			if ttl > cap {
				ttl = cap
			}
			ttl = lb.jitter(ttl)
			b.ttlUntil.Store(nowUnix + ttl)
			b.setState(stateIsolated, now)
			log.Printf("[backend:%s] probe trial fail: probing -> isolated (ttl=%ds)", b.Name, ttl)
		}
	}
}

// softUpDown handles the soft transitions: Degraded->Healthy after a clean
// window, consecErr decay after a clean window, and marking idle Healthy nodes
// as due-for-probe (logged; the external probe/traffic will exercise them).
func (lb *LoadBalancer) softUpDown(now time.Time) {
	nowUnix := now.Unix()
	for _, b := range lb.backends {
		s := b.state.Load()
		switch s {
		case stateDegraded:
			// Clean for the full window with recent successes -> Healthy.
			last := b.lastSuccessAt.Load()
			entered := b.enteredAt.Load()
			if last > 0 && last >= entered && nowUnix-entered >= cleanWindowSecs && b.consecErr.Load() == 0 {
				b.setState(stateHealthy, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] clean %ds: degraded -> healthy", b.Name, cleanWindowSecs)
			}
		case stateHealthy:
			// Decay consecErr after a clean window (no recent error).
			if b.consecErr.Load() > 0 {
				le := b.lastErr.Load()
				if nowUnix-le >= cleanWindowSecs {
					b.consecErr.Store(0)
				}
			}
			// Mark idle Healthy nodes (no success in idleProbeSecs) as due-for-probe.
			ls := b.lastSuccessAt.Load()
			if ls > 0 && nowUnix-ls >= idleProbeSecs {
				log.Printf("[backend:%s] idle >%ds: due for probe", b.Name, idleProbeSecs)
			}
		}
	}
}

// refreshWeights is a placeholder step: effective weights are computed on demand
// in effectiveWeight(), so there is no cached factor to refresh. Kept to preserve
// the documented five-step order and give a future cache a home.
func (lb *LoadBalancer) refreshWeights(now time.Time) {}

// routableCount returns the number of backends in {Healthy, Degraded, Probing}
// with a positive effective weight.
func (lb *LoadBalancer) routableCount() int {
	n := 0
	for _, b := range lb.backends {
		if isRoutable(b.state.Load()) && b.effectiveWeight() > 0 {
			n++
		}
	}
	return n
}

// candidateRank scores an Isolated/Quarantine backend for failsafe promotion.
// Lower rank sorts first (better candidate), per FR-023:
//
//	shortest remaining TTL -> transient(429/5xx) before quota(403) before
//	Quarantine/401 -> lower budget usage -> lower latency -> fewer consec.
type failsafeCandidate struct {
	b            *Backend
	ttlRemaining int64
	codeClass    int // 0=transient(429/5xx/transport), 1=403, 2=quarantine/401
	budgetRatio  float64
	latency      int64
	consec       int64
}

// failsafe enforces the minimum-availability guarantee (US3, FR-021..FR-026):
// when routable < failsafeTrigger, force-promote the best-ranked Isolated/
// Quarantine candidates to Probing (ignoring TTL) until routable >= min(
// failsafeTargetCap, total), shielding each for failsafeShieldSecs and recording
// lastFailsafeAt for hysteresis.
func (lb *LoadBalancer) failsafe(now time.Time) {
	nowUnix := now.Unix()

	total := len(lb.backends)
	if total == 0 {
		return
	}
	if lb.routableCount() >= failsafeTrigger {
		return
	}
	// Hysteresis: don't re-run within the window.
	if la := lb.lastFailsafeAt.Load(); la > 0 && nowUnix-la < failsafeHysteresisSecs {
		return
	}

	target := failsafeTargetCap
	if total < target {
		target = total
	}

	// Gather candidates from non-routable states.
	var cands []failsafeCandidate
	for _, b := range lb.backends {
		s := b.state.Load()
		if s != stateIsolated && s != stateQuarantine {
			continue
		}
		code := int(b.lastHTTPCode.Load())
		cc := 0
		switch {
		case s == stateQuarantine || code == 401:
			cc = 2
		case code == 403:
			cc = 1
		default:
			cc = 0
		}
		ratio := 0.0
		if lim := b.quotaLimit.Load(); lim > 0 {
			ratio = float64(b.quotaUsage.Load()) / float64(lim)
		}
		ttlRem := b.ttlUntil.Load() - nowUnix
		if ttlRem < 0 {
			ttlRem = 0
		}
		cands = append(cands, failsafeCandidate{
			b:            b,
			ttlRemaining: ttlRem,
			codeClass:    cc,
			budgetRatio:  ratio,
			latency:      b.lastLatencyMs.Load(),
			consec:       b.consecErr.Load(),
		})
	}
	if len(cands) == 0 {
		return
	}

	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.ttlRemaining != b.ttlRemaining {
			return a.ttlRemaining < b.ttlRemaining
		}
		if a.codeClass != b.codeClass {
			return a.codeClass < b.codeClass
		}
		if a.budgetRatio != b.budgetRatio {
			return a.budgetRatio < b.budgetRatio
		}
		if a.latency != b.latency {
			return a.latency < b.latency
		}
		return a.consec < b.consec
	})

	promoted := 0
	for _, c := range cands {
		if lb.routableCount() >= target {
			break
		}
		c.b.setState(stateProbing, now)
		c.b.ttlUntil.Store(0)
		c.b.probeShieldUntil.Store(nowUnix + failsafeShieldSecs)
		promoted++
		log.Printf("[backend:%s] failsafe: force-promoted to probing (shield %ds)", c.b.Name, failsafeShieldSecs)
	}
	if promoted > 0 {
		lb.lastFailsafeAt.Store(nowUnix)
		log.Printf("[failsafe] promoted %d backend(s) to keep routable >= %d", promoted, target)
	}
}

// ---------------------------------------------------------------------------
// External probe + quota (health/quota admin sync)
// ---------------------------------------------------------------------------

// SetHealthStatus is called by the external active health probe (check --health,
// via /admin/api/backends/health). It drives the 5-state machine (FR-016, FR-035):
//
//	healthy=true:  Isolated/Quarantine -> Probing; Probing -> Degraded;
//	               Degraded -> Healthy.
//	healthy=false: Healthy -> Degraded; Degraded -> Isolated (default TTL).
func (lb *LoadBalancer) SetHealthStatus(name string, healthy bool, latencyMs int64) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	now := lb.nowFn()
	for _, b := range lb.backends {
		if b.Name != name {
			continue
		}
		cur := b.state.Load()
		if healthy {
			switch cur {
			case stateIsolated, stateQuarantine, stateDisabled:
				b.setState(stateProbing, now)
				b.ttlUntil.Store(0)
				b.consecErr.Store(0)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe OK: %s -> probing (latency=%dms)", b.Name, stateName(cur), latencyMs)
			case stateProbing:
				b.setState(stateDegraded, now)
				b.consecErr.Store(0)
				log.Printf("[backend:%s] probe OK: probing -> degraded (latency=%dms)", b.Name, latencyMs)
			case stateDegraded:
				b.setState(stateHealthy, now)
				b.consecErr.Store(0)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe OK: degraded -> healthy (latency=%dms)", b.Name, latencyMs)
			default:
				// already healthy
			}
		} else {
			switch cur {
			case stateHealthy:
				b.setState(stateDegraded, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: healthy -> degraded", b.Name)
			case stateDegraded, stateProbing:
				lb.setIsolated(b, int(b.lastHTTPCode.Load()), 0, false, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: %s -> isolated", b.Name, stateName(cur))
			default:
				// already non-routable
			}
		}
		return true
	}
	return false
}

// MarkQuotaExhausted circuit-breaks a backend that reported an upstream
// "insufficient balance"/account-suspended error at request time. Kept for the
// request-path failover; it isolates the backend until the daily reset.
func (lb *LoadBalancer) MarkQuotaExhausted(name string) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	now := lb.nowFn()
	for _, b := range lb.backends {
		if b.Name == name {
			if !b.quotaExhausted.Swap(true) {
				b.quotaCheckedAt.Store(now.Unix())
				// Isolate until the daily reset (quota won't recover before then).
				b.ttlUntil.Store(now.Unix() + secondsUntilCSTMidnight(now))
				b.setState(stateIsolated, now)
				log.Printf("[backend:%s] quota exhausted (insufficient balance): isolated until daily reset", b.Name)
			}
			return true
		}
	}
	return false
}

// SetQuotaStatus updates the budget inputs for a named backend. Per FR-009 the
// budget only feeds the soft budgetFactor: exhausted=true no longer forces
// weight 0 or a state change.
func (lb *LoadBalancer) SetQuotaStatus(name string, exhausted bool, limit, usage float64) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name == name {
			b.quotaExhausted.Store(exhausted)
			b.quotaLimit.Store(int64(limit * 100))
			b.quotaUsage.Store(int64(usage * 100))
			b.quotaCheckedAt.Store(lb.nowFn().Unix())
			if exhausted {
				log.Printf("[backend:%s] quota near/at limit: usage=%.2f limit=%.2f (soft weight only)", b.Name, usage, limit)
			}
			return true
		}
	}
	return false
}

// quotaResetLoop clears quotaExhausted for all backends at 00:00 CST daily.
func (lb *LoadBalancer) quotaResetLoop() {
	cst := time.FixedZone("CST", 8*3600)
	for {
		now := time.Now().In(cst)
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, cst)
		time.Sleep(next.Sub(now))
		lb.mu.RLock()
		for _, b := range lb.backends {
			b.quotaExhausted.Store(false)
			b.quotaUsage.Store(0)
		}
		lb.mu.RUnlock()
		log.Printf("[quota] daily reset: all backends re-enabled")
	}
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// BackendInfo represents backend status info for API responses.
type BackendInfo struct {
	Name            string        `json:"name"`
	URL             string        `json:"url"`
	Weight          int           `json:"weight"`
	State           string        `json:"state"` // healthy|degraded|isolated|probing|quarantine
	EffectiveWeight int           `json:"effective_weight"`
	Disabled        bool          `json:"disabled"` // kept for backward compat: true when not selectable
	ErrCount        int64         `json:"err_count"`
	ConsecFailures  int64         `json:"consec_failures"`
	TTLUntil        int64         `json:"ttl_until"`      // cooldown expiry (unix s); 0 = none
	LastHTTPCode    int           `json:"last_http_code"` // most recent response code
	BudgetFactor    float64       `json:"budget_factor"`
	LatencyFactor   float64       `json:"latency_factor"`
	StatusCodeDist  map[int]int   `json:"status_code_dist"`
	StatusEntries   []StatusEntry `json:"status_entries"`
	RecentLatencyMs int64         `json:"recent_latency_ms"`
	ErrorRate       float64       `json:"error_rate"`
	QuotaExhausted  bool          `json:"quota_exhausted"`
	QuotaLimit      float64       `json:"quota_limit"`
	QuotaUsage      float64       `json:"quota_usage"`
	QuotaCheckedAt  int64         `json:"quota_checked_at"`
	DailyLimit      float64       `json:"daily_limit"`
}

// GetBackends returns all backends with their status info.
func (lb *LoadBalancer) GetBackends() []BackendInfo {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	result := make([]BackendInfo, 0, len(lb.backends))
	for _, b := range lb.backends {
		ew := b.effectiveWeight()
		result = append(result, BackendInfo{
			Name:            b.Name,
			URL:             b.URL,
			Weight:          b.Weight,
			State:           stateName(b.state.Load()),
			EffectiveWeight: ew,
			Disabled:        ew == 0,
			ErrCount:        b.consecErr.Load(),
			ConsecFailures:  b.consecErr.Load(),
			TTLUntil:        b.ttlUntil.Load(),
			LastHTTPCode:    int(b.lastHTTPCode.Load()),
			BudgetFactor:    budgetFactor(b.quotaUsage.Load(), b.quotaLimit.Load()),
			LatencyFactor:   latencyFactor(b.lastLatencyMs.Load()),
			StatusCodeDist:  b.GetStatusCodeDist(),
			StatusEntries:   b.GetStatusEntries(),
			RecentLatencyMs: b.GetRecentLatencyMs(),
			ErrorRate:       b.GetErrorRate(),
			QuotaExhausted:  b.quotaExhausted.Load(),
			QuotaLimit:      float64(b.quotaLimit.Load()) / 100,
			QuotaUsage:      float64(b.quotaUsage.Load()) / 100,
			QuotaCheckedAt:  b.quotaCheckedAt.Load(),
		})
	}
	return result
}

// GetBackendNames returns the names of all configured backends.
func (lb *LoadBalancer) GetBackendNames() []string {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	names := make([]string, 0, len(lb.backends))
	for _, b := range lb.backends {
		names = append(names, b.Name)
	}
	return names
}

// PickForPerfTest selects a routable backend and returns its connection details.
func (lb *LoadBalancer) PickForPerfTest() (name, url, apiKey string, client *http.Client, ok bool) {
	b := lb.Pick()
	if b == nil {
		return "", "", "", nil, false
	}
	return b.Name, b.URL, b.APIKey, b.Client(), true
}

// PickByNameForPerfTest selects a specific backend by name for perf testing.
func (lb *LoadBalancer) PickByNameForPerfTest(name string) (url, apiKey string, client *http.Client, ok bool) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name == name {
			return b.URL, b.APIKey, b.Client(), true
		}
	}
	return "", "", nil, false
}

// ---------------------------------------------------------------------------
// Validation (startup)
// ---------------------------------------------------------------------------

// ValidateBackends calls GET /v1/models on each backend and logs the result.
func (lb *LoadBalancer) ValidateBackends() {
	lb.mu.RLock()
	backends := make([]*Backend, len(lb.backends))
	copy(backends, lb.backends)
	lb.mu.RUnlock()

	for _, b := range backends {
		if ok := validateBackend(b); !ok {
			b.validationFailed.Store(true)
			log.Printf("[backend:%s] validate: permanently disabled due to validation failure", b.Name)
		}
	}
}

// UpdateBackends replaces the backend list with new config and validates them.
func (lb *LoadBalancer) UpdateBackends(cfgs []config.BackendAPI) {
	var newBackends []*Backend
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		newBackends = append(newBackends, lb.newBackend(c))
	}

	lb.mu.Lock()
	lb.backends = newBackends
	lb.mu.Unlock()

	lb.updateCapacities()
	lb.ValidateBackends()
}

// validateBackend returns true if the backend passes the /v1/models health check.
func validateBackend(b *Backend) bool {
	url := strings.TrimRight(b.URL, "/") + "/v1/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[backend:%s] validate: build request error: %v", b.Name, err)
		return false
	}
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	req.Header.Set("x-api-key", b.APIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[backend:%s] validate: request error: %v", b.Name, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[backend:%s] validate: FAIL — HTTP %d", b.Name, resp.StatusCode)
		return false
	}

	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[backend:%s] validate: decode response error: %v", b.Name, err)
		return false
	}

	if len(result.Data) == 0 {
		log.Printf("[backend:%s] validate: FAIL — data array is empty", b.Name)
		return false
	}

	log.Printf("[backend:%s] validate: OK — %d model(s) available", b.Name, len(result.Data))
	return true
}
