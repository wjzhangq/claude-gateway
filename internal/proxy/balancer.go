package proxy

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
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
	consecErrToDegrade    = 3 // reserved: passive path fast-ejects (see RecordResult); Degraded is entered via probe recovery / windowed rate (US11, FR-027)
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

// Feature 003 hardening tunables (see specs/003 research.md R-table). All are
// default-in-code; exposing them via config is optional and out of scope for v1.
const (
	// Quota isolation: default policy is a jittered long TTL + probe recovery
	// instead of a hardcoded wall-clock reset (FR-013/FR-014).
	quotaIsolationTTL = 6 * 60 * 60 // 6h base, jittered ±20%

	// Windowed error-rate signal for flapping backends (FR-018/FR-019).
	windowedMinSamples  = 8
	windowedDegradeRate = 0.5
	windowedIsolateRate = 0.8
	windowedRecoverRate = 0.3

	// Auth-quarantine exponential backoff cap (FR-020).
	authQuarantineMaxTTL = 24 * 60 * 60 // 24h

	// Bounded, budget-aware failover chain (FR-022/FR-023).
	failoverBudgetSecs    = 20 // total wall-clock budget across a request's failover chain
	quotaCascadeThreshold = 3  // this many quota reports within the window => fast-fail
	cascadeWindowSecs     = 10

	// Retry-After honored bounds (FR-025/FR-026).
	retryAfterMinSecs = 5
	retryAfterMaxSecs = 30 * 60 // 30min

	// Startup validationFailed re-check cadence (FR-011 secondary safety net).
	revalidatePeriodSecs = 60 * 60 // hourly
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
	quotaReset       string // "" (long-TTL), "cst-midnight", "utc-midnight" (FR-014)
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

	// Feature 003 observability + backoff state (FR-020, FR-028..FR-030).
	totalErr             atomic.Int64    // cumulative impacting errors (never reset at runtime); distinct from consecErr
	authQuarantineCount  atomic.Int64    // consecutive auth (401) quarantines; drives exponential TTL
	failsafePromotions   atomic.Int64    // times force-promoted by the failsafe
	probeCostCents       atomic.Int64    // accumulated estimated inference-probe cost (cents)
	estSpendAt403Cents   atomic.Int64    // quotaUsage snapshot at first quota-403 (0 = not seen)
	limitAt403Cents      atomic.Int64    // quotaLimit snapshot at first quota-403
	dwellByState         [6]atomic.Int64 // accumulated seconds spent in each state
	isolationCountByCode map[int]int64   // per-code isolation tally (guarded by metricsMu)
	metricsMu            sync.Mutex
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
	return secondsUntilMidnight(now, 8*3600)
}

// secondsUntilMidnight returns seconds from now until the next 00:00 in the
// fixed zone at offsetSecs east of UTC.
func secondsUntilMidnight(now time.Time, offsetSecs int) int64 {
	zone := time.FixedZone("z", offsetSecs)
	n := now.In(zone)
	next := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, zone)
	return int64(next.Sub(n).Seconds())
}

// quotaIsolationSecs returns the cooldown (seconds) for a quota-isolated backend
// per its configured reset policy (FR-013/FR-014). Default ("" policy) is a
// jittered long TTL so recovery is discovered by probe/releaseExpired rather
// than bound to a single hardcoded wall-clock instant that may not match the
// backend's real reset.
func (lb *LoadBalancer) quotaIsolationSecs(b *Backend, now time.Time) int64 {
	switch b.quotaReset {
	case "cst-midnight":
		return secondsUntilCSTMidnight(now)
	case "utc-midnight":
		return secondsUntilMidnight(now, 0)
	default:
		return lb.jitter(quotaIsolationTTL)
	}
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
//   - retryAfterUnix > 0: honor the 429 Retry-After, clamped to [retryAfterMinSecs,
//     retryAfterMaxSecs] so one header can neither remove a node for hours (FR-025)
//     nor cause a hot re-probe loop (FR-026).
//   - quotaBody: a 403 whose body clearly indicates quota → per-backend quota
//     reset policy (default: jittered long TTL), not a single hardcoded clock (FR-013).
func (lb *LoadBalancer) computeTTL(b *Backend, code int, retryAfterUnix int64, quotaBody bool, now time.Time) int64 {
	nowUnix := now.Unix()
	// 429 Retry-After takes precedence: honor the absolute expiry, clamped.
	if code == 429 && retryAfterUnix > nowUnix {
		secs := retryAfterUnix - nowUnix
		if secs > retryAfterMaxSecs {
			secs = retryAfterMaxSecs
		}
		if secs < retryAfterMinSecs {
			secs = retryAfterMinSecs
		}
		return secs
	}
	// 403 quota body → recover per the backend's configured quota-reset policy.
	if code == 403 && quotaBody {
		return lb.quotaIsolationSecs(b, now)
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
// Degraded triggers (FR-027): the passive request path does NOT enter Degraded —
// the first impacting error isolates directly (fast-eject is intentional). The
// Degraded state is reachable via three documented paths, all verified in tests:
//  1. Probe recovery: Isolated/Quarantine -> Probing -> Degraded (SetHealthStatus true).
//  2. Windowed error rate >= windowedDegradeRate on a Healthy node (softUpDown, US7).
//  3. Active probe failure on a Healthy node: Healthy -> Degraded (SetHealthStatus false).
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
		b.authQuarantineCount.Store(0) // a success clears auth-backoff escalation (FR-020)
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
		b.totalErr.Add(1)
		b.consecOK.Store(0)
		b.consecErr.Add(1)
		lb.setQuarantine(b, 401, now, "auth failure (401)")
		lb.eventFailsafe(now)
		return

	default: // ErrRateLimit, ErrServer, ErrTransport, ErrForbidden
		b.lastErr.Store(now.Unix())
		b.totalErr.Add(1)
		b.consecOK.Store(0)
		n := b.consecErr.Add(1)

		// A shielded, freshly failsafe-promoted Probing node is not re-isolated.
		if b.state.Load() == stateProbing && now.Unix() < b.probeShieldUntil.Load() {
			return
		}

		// Consecutive-failure escalation to Quarantine regardless of class.
		if n >= consecErrToQuarantine {
			lb.setQuarantine(b, int(b.lastHTTPCode.Load()), now, "5 consecutive failures")
			lb.eventFailsafe(now)
			return
		}

		// Calibration snapshot (FR-030): on the first quota-indicating 403, record
		// the estimated spend and configured limit so budget-estimate error can be
		// measured after the fact.
		if class == ErrForbidden && quotaBody && b.estSpendAt403Cents.Load() == 0 {
			b.estSpendAt403Cents.Store(b.quotaUsage.Load())
			b.limitAt403Cents.Store(b.quotaLimit.Load())
		}

		code := httpCodeForClass(class)
		if lc := b.lastHTTPCode.Load(); lc != 0 && (class == ErrServer || class == ErrRateLimit || class == ErrForbidden) {
			code = int(lc)
		}
		lb.setIsolated(b, code, retryAfterUnix, quotaBody, now)
		lb.eventFailsafe(now)
	}
}

// eventFailsafe runs the failsafe immediately after a request-path transition
// into a non-routable state, closing the ~60s window that waiting for the next
// reconcile tick would open (FR-001). It is called from recordResult, which
// holds no lock, so it takes its own RLock; the reconcile/probe callers that
// already hold lb.mu invoke lb.failsafe directly instead.
//
// It only intervenes on fleets larger than the trigger: on a fleet with
// failsafeTrigger or fewer nodes, "routable < trigger" is the fleet's steady
// state (it can never reach the floor), so synchronously promoting a
// just-isolated node would only churn the request path. Such small fleets rely
// on the 60s reconcile pass, which promotes without this size gate.
func (lb *LoadBalancer) eventFailsafe(now time.Time) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	if len(lb.backends) > failsafeTrigger && lb.routableCount() < failsafeTrigger {
		lb.failsafe(now)
	}
}

// setState stores a new state and records the entry timestamp.
func (b *Backend) setState(s int32, now time.Time) {
	b.state.Store(s)
	b.enteredAt.Store(now.Unix())
}

// setIsolated moves a backend to Isolated with a code-specific TTL. No-op alert.
func (lb *LoadBalancer) setIsolated(b *Backend, code int, retryAfterUnix int64, quotaBody bool, now time.Time) {
	ttl := lb.computeTTL(b, code, retryAfterUnix, quotaBody, now)
	b.ttlUntil.Store(now.Unix() + ttl)
	prev := b.state.Swap(stateIsolated)
	if prev != stateIsolated {
		b.accrueDwell(prev, now)
		b.noteIsolation(code)
		log.Printf("[backend:%s] isolated: code=%d ttl=%ds", b.Name, code, ttl)
	}
	b.enteredAt.Store(now.Unix())
}

// noteIsolation increments the per-code isolation tally for metrics (FR-029).
func (b *Backend) noteIsolation(code int) {
	b.metricsMu.Lock()
	if b.isolationCountByCode == nil {
		b.isolationCountByCode = make(map[int]int64)
	}
	b.isolationCountByCode[code]++
	b.metricsMu.Unlock()
}

// accrueDwell adds the time spent in prevState (the state being left) to its
// dwell bucket (FR-029). Called on state exit, before enteredAt is reset.
func (b *Backend) accrueDwell(prevState int32, now time.Time) {
	if prevState < 0 || int(prevState) >= len(b.dwellByState) {
		return
	}
	entered := b.enteredAt.Load()
	if entered > 0 && now.Unix() > entered {
		b.dwellByState[prevState].Add(now.Unix() - entered)
	}
}

// setQuarantine moves a backend to Quarantine with a long TTL and raises an alert
// on entry (FR-038). For auth (401) quarantines the TTL grows exponentially with
// the repeat count (FR-020): a revoked key does not self-heal, so re-probing it
// every 15m is wasted; back off 15m -> 30m -> 1h ... capped at authQuarantineMaxTTL.
// The count resets on any 2xx (recordResult ErrNone) or a key-rotation signal
// (ResetAuthQuarantine).
func (lb *LoadBalancer) setQuarantine(b *Backend, code int, now time.Time, reason string) {
	base, _ := baseCapForCode(401) // Quarantine always uses the 401 TTL band
	var ttl int64
	if code == 401 {
		shift := b.authQuarantineCount.Add(1) - 1 // 0 on first quarantine
		if shift > 30 {
			shift = 30 // guard against int64 overflow on the left shift
		}
		grown := base << uint(shift)
		if grown > authQuarantineMaxTTL || grown <= 0 {
			grown = authQuarantineMaxTTL
		}
		ttl = lb.jitter(grown)
	} else {
		ttl = lb.jitter(base)
	}
	b.ttlUntil.Store(now.Unix() + ttl)
	prev := b.state.Swap(stateQuarantine)
	if prev != stateQuarantine {
		b.accrueDwell(prev, now)
		b.noteIsolation(code)
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

	quotaReportMu    sync.Mutex // guards quotaReportTimes
	quotaReportTimes []int64    // unix s of recent quota reports (cascade fast-fail, FR-023)
}

// noteQuotaReport records that a backend just reported a quota failure, for the
// cascade fast-fail detector (FR-023).
func (lb *LoadBalancer) noteQuotaReport(now time.Time) {
	lb.quotaReportMu.Lock()
	defer lb.quotaReportMu.Unlock()
	cutoff := now.Unix() - cascadeWindowSecs
	kept := lb.quotaReportTimes[:0]
	for _, t := range lb.quotaReportTimes {
		if t >= cutoff {
			kept = append(kept, t)
		}
	}
	lb.quotaReportTimes = append(kept, now.Unix())
}

// quotaCascadeActive reports whether at least quotaCascadeThreshold backends
// reported quota within the last cascadeWindowSecs — the signal to fast-fail a
// request instead of walking the remaining backends and adding load at the worst
// moment (FR-023).
func (lb *LoadBalancer) quotaCascadeActive(now time.Time) bool {
	lb.quotaReportMu.Lock()
	defer lb.quotaReportMu.Unlock()
	cutoff := now.Unix() - cascadeWindowSecs
	n := 0
	for _, t := range lb.quotaReportTimes {
		if t >= cutoff {
			n++
		}
	}
	return n >= quotaCascadeThreshold
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
	go lb.revalidateLoop()
	return lb
}

// revalidateLoop periodically re-checks backends permanently marked
// validationFailed at startup and clears the flag if they now pass the shared
// reachability probe (FR-011 secondary safety net). This heals a transient
// startup failure that would otherwise silently keep a usable backend disabled
// forever. It runs off the hot path and performs its own network I/O, so it is
// a separate goroutine rather than a step in the synchronous Reconcile pass.
func (lb *LoadBalancer) revalidateLoop() {
	ticker := time.NewTicker(revalidatePeriodSecs * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		lb.mu.RLock()
		failed := make([]*Backend, 0)
		for _, b := range lb.backends {
			if b.validationFailed.Load() {
				failed = append(failed, b)
			}
		}
		lb.mu.RUnlock()
		for _, b := range failed {
			if validateBackend(b) {
				b.validationFailed.Store(false)
				log.Printf("[backend:%s] revalidate: recovered, re-enabled", b.Name)
			}
		}
	}
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
		Name:       c.Name,
		URL:        c.URL,
		APIKey:     c.APIKey,
		Weight:     c.Weight,
		quotaReset: c.QuotaReset,
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
	if best == nil && len(lb.backends) > failsafeTrigger {
		// No routable candidate on a fleet large enough for the floor to be
		// meaningful: synchronously run the failsafe (FR-002) to force-promote
		// recoverable backends, then re-scan once. This closes the availability
		// window that would otherwise wait for the 60s reconcile. We already hold
		// lb.mu.RLock; failsafe only performs atomic writes, so it is safe here
		// without upgrading the lock. Small fleets (<= trigger) fall through and
		// return nil, preserving the "isolated node is unselectable" contract.
		lb.failsafe(lb.nowFn())
		total = 0
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

// windowedErrorRate returns the impacting-error rate (403/429/5xx/transport==0)
// and sample count over the recent ring-buffer window (recentCodes). It gives a
// flapping backend — one that alternates success/failure so consecErr never
// accumulates — a rate-based trigger for degrade/isolate (US7, FR-018).
func (b *Backend) windowedErrorRate(now time.Time) (rate float64, samples int) {
	codes := b.recentCodes(now)
	if len(codes) == 0 {
		return 0, 0
	}
	errs := 0
	for _, c := range codes {
		if c == 403 || c == 429 || c >= 500 || c == 0 {
			errs++
		}
	}
	return float64(errs) / float64(len(codes)), len(codes)
}

// softUpDown handles the soft transitions: Degraded->Healthy after a clean
// window, consecErr decay after a clean window, the windowed error-rate signal
// for flapping backends (US7), and marking idle Healthy nodes as due-for-probe.
func (lb *LoadBalancer) softUpDown(now time.Time) {
	nowUnix := now.Unix()
	for _, b := range lb.backends {
		// Windowed error-rate signal (FR-018): catch flappers that never build a
		// consecutive run. Only acts on routable states; a sustained high rate
		// degrades, a very high rate isolates. Recovery is gated below (< recover).
		rate, samples := b.windowedErrorRate(now)
		if samples >= windowedMinSamples && isRoutable(b.state.Load()) {
			if rate >= windowedIsolateRate {
				lb.setIsolated(b, int(b.lastHTTPCode.Load()), 0, false, now)
				log.Printf("[backend:%s] windowed error rate %.0f%% (n=%d): -> isolated", b.Name, rate*100, samples)
				continue
			}
			if rate >= windowedDegradeRate && b.state.Load() == stateHealthy {
				b.setState(stateDegraded, now)
				log.Printf("[backend:%s] windowed error rate %.0f%% (n=%d): healthy -> degraded", b.Name, rate*100, samples)
			}
		}

		s := b.state.Load()
		switch s {
		case stateDegraded:
			// Clean for the full window with recent successes AND a windowed rate
			// below the recovery threshold -> Healthy.
			last := b.lastSuccessAt.Load()
			entered := b.enteredAt.Load()
			rateOK := samples < windowedMinSamples || rate < windowedRecoverRate
			if last > 0 && last >= entered && nowUnix-entered >= cleanWindowSecs && b.consecErr.Load() == 0 && rateOK {
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
//
// Promotion is one-shot: a single invocation promotes as many candidates as
// needed to reach min(failsafeTargetCap=7, total) in one pass, not one node per
// cycle. Feature 003 (FR-001/FR-002) also invokes it event-driven — from
// eventFailsafe on a request-path transition into a non-routable state, and
// synchronously from pick() when no routable candidate is found — so recovery no
// longer waits up to 60s for the reconcile tick. Callers already holding lb.mu
// (Reconcile, pick) call this directly; eventFailsafe takes its own RLock.
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
		c.b.accrueDwell(c.b.state.Load(), now)
		c.b.setState(stateProbing, now)
		c.b.ttlUntil.Store(0)
		c.b.probeShieldUntil.Store(nowUnix + failsafeShieldSecs)
		c.b.failsafePromotions.Add(1)
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

// SetHealthStatus is the compatibility wrapper for callers that do not carry the
// probe-observed code / Retry-After (older check binaries). It delegates to
// SetHealthStatusDetailed with observedCode=0 (server-band fallback).
func (lb *LoadBalancer) SetHealthStatus(name string, healthy bool, latencyMs int64) bool {
	return lb.SetHealthStatusDetailed(name, healthy, latencyMs, 0, 0)
}

// SetHealthStatusDetailed is called by the external active health probe
// (check --health, via /admin/api/backends/health). It drives the 5-state
// machine (FR-016, FR-035) and, on failure, isolates using the SAME computeTTL
// policy as the passive request path (FR-005..FR-007): the probe-observed HTTP
// code and Retry-After determine the cooldown, so a probe-observed 429 honors
// its Retry-After instead of being mis-bucketed as a short server-band cooldown.
//
//	healthy=true:  Isolated/Quarantine -> Probing; Probing -> Degraded;
//	               Degraded -> Healthy.
//	healthy=false: Healthy -> Degraded; Degraded/Probing -> Isolated
//	               (TTL from observedCode + retryAfterUnix; 0 => server band).
func (lb *LoadBalancer) SetHealthStatusDetailed(name string, healthy bool, latencyMs int64, observedCode int, retryAfterUnix int64) bool {
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
			// Prefer the probe-observed code; fall back to the last recorded live
			// code, then to 0 (server band) — matching the passive path's TTL.
			code := observedCode
			if code == 0 {
				code = int(b.lastHTTPCode.Load())
			}
			switch cur {
			case stateHealthy:
				b.setState(stateDegraded, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: healthy -> degraded", b.Name)
			case stateDegraded, stateProbing:
				lb.setIsolated(b, code, retryAfterUnix, false, now)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: %s -> isolated (code=%d)", b.Name, stateName(cur), code)
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
	lb.noteQuotaReport(now) // feed the cascade fast-fail detector (FR-023)
	for _, b := range lb.backends {
		if b.Name == name {
			if !b.quotaExhausted.Swap(true) {
				b.quotaCheckedAt.Store(now.Unix())
				// Isolate per the backend's quota-reset policy (default: jittered
				// long TTL + probe recovery), not a single hardcoded clock (FR-013).
				prev := b.state.Swap(stateIsolated)
				b.accrueDwell(prev, now)
				b.ttlUntil.Store(now.Unix() + lb.quotaIsolationSecs(b, now))
				b.enteredAt.Store(now.Unix())
				b.noteIsolation(403)
				log.Printf("[backend:%s] quota exhausted (insufficient balance): isolated per quota_reset=%q", b.Name, b.quotaReset)
			}
			return true
		}
	}
	return false
}

// QuotaCascadeActive reports whether a quota cascade is in progress (FR-023),
// exposed for the request-path failover fast-fail.
func (lb *LoadBalancer) QuotaCascadeActive() bool {
	return lb.quotaCascadeActive(lb.nowFn())
}

// AddProbeCost attributes an inference-probe's estimated cost (USD) to a named
// backend's spend accounting so budget stays closed (FR-010).
func (lb *LoadBalancer) AddProbeCost(name string, costUSD float64) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name == name {
			b.probeCostCents.Add(int64(costUSD * 100))
			return true
		}
	}
	return false
}

// ResetAuthQuarantine clears the exponential auth-quarantine backoff for a named
// backend and drops its cooldown so a rotated key is retried immediately (FR-021).
func (lb *LoadBalancer) ResetAuthQuarantine(name string) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	now := lb.nowFn()
	for _, b := range lb.backends {
		if b.Name == name {
			b.authQuarantineCount.Store(0)
			if b.state.Load() == stateQuarantine {
				b.setState(stateProbing, now)
				b.ttlUntil.Store(0)
				log.Printf("[backend:%s] key rotated: quarantine -> probing", b.Name)
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
			ErrCount:        b.totalErr.Load(),
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

// BackendMetrics is the per-backend observability snapshot exposed via
// /admin/api/backends/metrics for diagnosing the state machine and calibrating
// the budget estimate (US12, FR-028..FR-030). In-memory only; resets on restart.
type BackendMetrics struct {
	Name                  string           `json:"name"`
	TotalErr              int64            `json:"total_err"`
	ConsecErr             int64            `json:"consec_err"`
	IsolationCountByCode  map[string]int64 `json:"isolation_count_by_code"`
	FailsafePromotions    int64            `json:"failsafe_promotions"`
	ProbeCostUSD          float64          `json:"probe_cost_usd"`
	DwellSecondsByState   map[string]int64 `json:"dwell_seconds_by_state"`
	EstSpendAtFirst403USD float64          `json:"est_spend_at_first_403_usd"`
	LimitAtFirst403USD    float64          `json:"limit_at_first_403_usd"`
}

// Metrics returns the per-backend observability snapshot for all backends.
func (lb *LoadBalancer) Metrics() []BackendMetrics {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	out := make([]BackendMetrics, 0, len(lb.backends))
	for _, b := range lb.backends {
		b.metricsMu.Lock()
		isoByCode := make(map[string]int64, len(b.isolationCountByCode))
		for code, n := range b.isolationCountByCode {
			isoByCode[strconv.Itoa(code)] = n
		}
		b.metricsMu.Unlock()

		dwell := make(map[string]int64, len(b.dwellByState))
		for s := range b.dwellByState {
			dwell[stateName(int32(s))] = b.dwellByState[s].Load()
		}

		out = append(out, BackendMetrics{
			Name:                  b.Name,
			TotalErr:              b.totalErr.Load(),
			ConsecErr:             b.consecErr.Load(),
			IsolationCountByCode:  isoByCode,
			FailsafePromotions:    b.failsafePromotions.Load(),
			ProbeCostUSD:          float64(b.probeCostCents.Load()) / 100,
			DwellSecondsByState:   dwell,
			EstSpendAtFirst403USD: float64(b.estSpendAt403Cents.Load()) / 100,
			LimitAtFirst403USD:    float64(b.limitAt403Cents.Load()) / 100,
		})
	}
	return out
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
// validateBackend judges a backend usable via the SAME reachability logic as the
// runtime active probe (FR-011): GET /v1/models, falling back to a minimal
// /v1/messages inference call. This ensures a backend that serves inference but
// does not support the listing endpoint is NOT permanently disabled at startup
// (FR-012) — the previous listing-only check silently lost such capacity.
func validateBackend(b *Backend) bool {
	client := &http.Client{Timeout: 15 * time.Second}
	code, _, err := ProbeReachable(client, b.URL, b.APIKey)
	if err != nil {
		log.Printf("[backend:%s] validate: FAIL — code=%d: %v", b.Name, code, err)
		return false
	}
	log.Printf("[backend:%s] validate: OK (code=%d)", b.Name, code)
	return true
}
