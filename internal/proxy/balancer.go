package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"strings"
	"net/http"
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
// request outcomes and external health probes.
const (
	stateHealthy  int32 = 0 // full weight
	stateDegraded int32 = 1 // reduced weight (Weight/4)
	stateDisabled int32 = 2 // not selectable; recovers only via active probe
)

// degradedWeightDivisor is applied to a backend's weight while degraded.
const degradedWeightDivisor = 4

// Thresholds for state transitions driven by consecutive request outcomes.
const (
	consecErrToDegrade = 3 // consecutive errors: healthy -> degraded
	consecErrToDisable = 5 // consecutive errors: -> disabled
	consecOKToHealthy  = 3 // consecutive successes: degraded -> healthy
)

// ErrorClass categorizes a request outcome for health accounting.
type ErrorClass int

const (
	ErrNone      ErrorClass = iota // 2xx success
	ErrClient                      // 4xx (non-401/403/429): caller's fault, ignored
	ErrAuth                        // 401/403: backend key invalid -> disable
	ErrRateLimit                   // 429: transient, counts as error
	ErrServer                      // 5xx: upstream fault, counts as error
	ErrTransport                   // connection-level failure, counts as error
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
	case statusCode == 401 || statusCode == 403:
		return ErrAuth
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
	case ErrRateLimit, ErrServer, ErrTransport:
		return true
	default:
		return false
	}
}

// Backend represents a single upstream API endpoint with its HTTP client.
type Backend struct {
	Name            string
	URL             string
	APIKey          string
	Weight          int
	client          *http.Client
	state           atomic.Int32 // stateHealthy/stateDegraded/stateDisabled
	consecErr       atomic.Int64 // consecutive health-impacting errors
	consecOK        atomic.Int64 // consecutive successes (for degraded->healthy)
	lastErr         atomic.Int64 // unix timestamp of last error
	lastLatencyMs   atomic.Int64 // latency of the most recent request in ms
	validationFailed atomic.Bool  // set on startup validation failure; never auto-recovered
	quotaExhausted  atomic.Bool   // set by external check when daily quota is exceeded
	quotaLimit      atomic.Int64  // hard_limit_usd * 100 (cents precision)
	quotaUsage      atomic.Int64  // total_usage * 100 (cents precision)
	quotaCheckedAt  atomic.Int64  // unix timestamp of last quota check
	statusEntries   []StatusEntry // ring buffer of recent requests (code + latency + timestamp)
	statusCodeDist  map[int]int   // distribution of status codes within the ring buffer
	statusCapacity  int           // dynamic capacity: max(50, 10 * backends_count)
	statusMu        sync.Mutex
}

// Client returns the backend's dedicated HTTP client.
func (b *Backend) Client() *http.Client { return b.client }

// RecordResult updates the backend health state based on a classified outcome.
//   - ErrNone: reset consecutive errors; if degraded, count successes toward recovery.
//   - ErrClient/ErrCanceled: ignored entirely (caller's request was bad or aborted).
//   - ErrAuth: disable immediately (backend key invalid).
//   - ErrRateLimit/ErrServer/ErrTransport: count as an error and maybe degrade/disable.
func (b *Backend) RecordResult(class ErrorClass) {
	switch class {
	case ErrClient, ErrCanceled:
		// Not the backend's fault (bad request, or the client aborted mid-flight)
		// — leave health untouched.
		return
	case ErrNone:
		b.consecErr.Store(0)
		// Promote degraded -> healthy after enough consecutive successes.
		if b.state.Load() == stateDegraded {
			if b.consecOK.Add(1) >= consecOKToHealthy {
				b.state.Store(stateHealthy)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] recovered: degraded -> healthy", b.Name)
			}
		} else {
			b.consecOK.Store(0)
		}
		return
	case ErrAuth:
		b.lastErr.Store(time.Now().Unix())
		b.consecOK.Store(0)
		if b.state.Swap(stateDisabled) != stateDisabled {
			log.Printf("[backend:%s] disabled: auth failure (401/403)", b.Name)
		}
		return
	default: // ErrRateLimit, ErrServer, ErrTransport
		b.lastErr.Store(time.Now().Unix())
		b.consecOK.Store(0)
		n := b.consecErr.Add(1)
		switch {
		case n >= consecErrToDisable:
			if b.state.Swap(stateDisabled) != stateDisabled {
				log.Printf("[backend:%s] disabled: %d consecutive errors", b.Name, n)
			}
		case n >= consecErrToDegrade:
			if b.state.CompareAndSwap(stateHealthy, stateDegraded) {
				log.Printf("[backend:%s] degraded: %d consecutive errors", b.Name, n)
			}
		}
	}
}

// SetState forces the backend into a specific health state (used by active probes).
func (b *Backend) SetState(s int32) {
	b.state.Store(s)
	if s == stateHealthy {
		b.consecErr.Store(0)
		b.consecOK.Store(0)
	}
}

// State returns the current health state.
func (b *Backend) State() int32 { return b.state.Load() }

// effectiveWeight returns the selection weight after applying the health state.
// Returns 0 when the backend must not be selected.
func (b *Backend) effectiveWeight() int {
	if b.validationFailed.Load() || b.quotaExhausted.Load() {
		return 0
	}
	switch b.state.Load() {
	case stateDisabled:
		return 0
	case stateDegraded:
		w := b.Weight / degradedWeightDivisor
		if w < 1 {
			w = 1 // keep a sliver of traffic to allow passive recovery
		}
		return w
	default:
		return b.Weight
	}
}

// RecordRequest records a request outcome (status code + latency) in the ring buffer.
// It replaces the old RecordStatusCode and also tracks request timestamps.
func (b *Backend) RecordRequest(code int, latencyMs int64) {
	b.lastLatencyMs.Store(latencyMs)
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
		Timestamp: time.Now().Unix(),
	})
	if len(b.statusEntries) > cap {
		// Evict oldest entry and update distribution
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
// It delegates to RecordRequest with latency=0.
func (b *Backend) RecordStatusCode(code int) {
	b.RecordRequest(code, 0)
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

// BackendInfo represents backend status info for API responses.
type BackendInfo struct {
	Name            string        `json:"name"`
	URL             string        `json:"url"`
	Weight          int           `json:"weight"`
	State           string        `json:"state"`     // "healthy" | "degraded" | "disabled"
	EffectiveWeight int           `json:"effective_weight"`
	Disabled        bool          `json:"disabled"`  // kept for backward compat: true when not selectable
	ErrCount        int64         `json:"err_count"` // consecutive health-impacting errors
	StatusCodeDist  map[int]int   `json:"status_code_dist"`
	StatusEntries   []StatusEntry `json:"status_entries"`    // ring buffer: code + latency + timestamp
	RecentLatencyMs int64         `json:"recent_latency_ms"` // latency of the last request
	ErrorRate       float64       `json:"error_rate"`
	QuotaExhausted  bool          `json:"quota_exhausted"`
	QuotaLimit      float64       `json:"quota_limit"`
	QuotaUsage      float64       `json:"quota_usage"`
	QuotaCheckedAt  int64         `json:"quota_checked_at"`
	DailyLimit      float64       `json:"daily_limit"` // configured per-backend daily cost cap (USD); 0 = unlimited
}

// stateName converts a numeric state to its string label.
func stateName(s int32) string {
	switch s {
	case stateDegraded:
		return "degraded"
	case stateDisabled:
		return "disabled"
	default:
		return "healthy"
	}
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

// LoadBalancer selects backends using weighted random selection with health tracking.
type LoadBalancer struct {
	mu       sync.RWMutex
	backends []*Backend
}

// NewLoadBalancer builds backends from config and starts the recovery ticker.
func NewLoadBalancer(cfgs []config.BackendAPI) *LoadBalancer {
	lb := &LoadBalancer{}
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // prevent gzip on SSE streams (e.g. MiniMax returns compressed binary)
		}
		lb.backends = append(lb.backends, &Backend{
			Name:   c.Name,
			URL:    c.URL,
			APIKey: c.APIKey,
			Weight: c.Weight,
			client: &http.Client{
				Transport: transport,
				Timeout:   300 * time.Second, // long for streaming
			},
		})
	}
	lb.updateCapacities()
	go lb.quotaResetLoop()
	return lb
}

// updateCapacities sets each backend's ring-buffer capacity to max(minStatusCodes, 10*backends_count).
// Must be called after lb.backends is populated (without holding the write lock).
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

// Pick selects a healthy backend using weighted random selection.
// Returns nil if no healthy backend is available.
// Uses two-pass scan to avoid per-call slice allocation.
func (lb *LoadBalancer) Pick() *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// First pass: compute total effective weight of selectable backends
	totalWeight := 0
	for _, b := range lb.backends {
		totalWeight += b.effectiveWeight()
	}
	if totalWeight == 0 {
		return nil
	}

	// Second pass: weighted selection
	r := rand.Intn(totalWeight)
	for _, b := range lb.backends {
		w := b.effectiveWeight()
		if w == 0 {
			continue
		}
		r -= w
		if r < 0 {
			return b
		}
	}
	// Fallback: return last selectable backend (handles rounding edge cases)
	for i := len(lb.backends) - 1; i >= 0; i-- {
		if lb.backends[i].effectiveWeight() > 0 {
			return lb.backends[i]
		}
	}
	return nil
}

// PickExcluding is like Pick but skips any backend whose name is in exclude.
// Used by quota failover to route around a backend that just reported an
// upstream "insufficient balance" error. Returns nil when every remaining
// backend is unselectable.
func (lb *LoadBalancer) PickExcluding(exclude map[string]bool) *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	totalWeight := 0
	for _, b := range lb.backends {
		if exclude[b.Name] {
			continue
		}
		totalWeight += b.effectiveWeight()
	}
	if totalWeight == 0 {
		return nil
	}

	r := rand.Intn(totalWeight)
	for _, b := range lb.backends {
		if exclude[b.Name] {
			continue
		}
		w := b.effectiveWeight()
		if w == 0 {
			continue
		}
		r -= w
		if r < 0 {
			return b
		}
	}
	for i := len(lb.backends) - 1; i >= 0; i-- {
		b := lb.backends[i]
		if !exclude[b.Name] && b.effectiveWeight() > 0 {
			return b
		}
	}
	return nil
}

// recoveryLoop previously auto-revived disabled backends after 30s of silence.
// That behavior is removed: disabled backends now recover ONLY via the external
// active health probe (SetHealthStatus), and degraded backends recover passively
// through consecutive successful requests. This loop is retained as a no-op hook
// point and currently does nothing; recovery is event-driven.
func (lb *LoadBalancer) recoveryLoop() {
	// intentionally empty: recovery is driven by RecordResult (passive) and
	// SetHealthStatus (active probe). Kept for symmetry with quotaResetLoop.
}

// PickForPerfTest selects a healthy backend and returns its connection details for perf testing.
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

// ValidateBackends calls GET /v1/models on each backend and logs the result.
// Backends that fail validation (non-200, missing/empty data array) are disabled (weight=0).
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
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // prevent gzip on SSE streams (e.g. MiniMax returns compressed binary)
		}
		newBackends = append(newBackends, &Backend{
			Name:   c.Name,
			URL:    c.URL,
			APIKey: c.APIKey,
			Weight: c.Weight,
			client: &http.Client{
				Transport: transport,
				Timeout:   300 * time.Second,
			},
		})
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

// SetHealthStatus is called by the external active health probe to report
// whether a backend is reachable. State transitions:
//   - healthy=true, disabled  -> degraded (half-recovered, low weight to verify)
//   - healthy=true, degraded  -> healthy  (fully recovered)
//   - healthy=false, healthy  -> degraded
//   - healthy=false, degraded -> disabled
func (lb *LoadBalancer) SetHealthStatus(name string, healthy bool, latencyMs int64) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name != name {
			continue
		}
		cur := b.state.Load()
		if healthy {
			switch cur {
			case stateDisabled:
				b.state.Store(stateDegraded)
				b.consecErr.Store(0)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe OK: disabled -> degraded (latency=%dms)", b.Name, latencyMs)
			case stateDegraded:
				b.state.Store(stateHealthy)
				b.consecErr.Store(0)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe OK: degraded -> healthy (latency=%dms)", b.Name, latencyMs)
			default:
				// already healthy, nothing to do
			}
		} else {
			switch cur {
			case stateHealthy:
				b.state.Store(stateDegraded)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: healthy -> degraded", b.Name)
			case stateDegraded:
				b.state.Store(stateDisabled)
				b.consecOK.Store(0)
				log.Printf("[backend:%s] probe FAIL: degraded -> disabled", b.Name)
			default:
				// already disabled, nothing to do
			}
		}
		return true
	}
	return false
}

// MarkQuotaExhausted circuit-breaks a backend that reported an upstream
// "insufficient balance"/account-suspended error at request time. The backend
// becomes unselectable until the daily quotaResetLoop clears it (or an external
// quota probe re-enables it). Returns false if no backend matches name.
func (lb *LoadBalancer) MarkQuotaExhausted(name string) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name == name {
			if !b.quotaExhausted.Swap(true) {
				b.quotaCheckedAt.Store(time.Now().Unix())
				log.Printf("[backend:%s] quota exhausted (insufficient balance): circuit-broken until daily reset", b.Name)
			}
			return true
		}
	}
	return false
}

// SetQuotaStatus updates the quota state for a named backend.
func (lb *LoadBalancer) SetQuotaStatus(name string, exhausted bool, limit, usage float64) bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	for _, b := range lb.backends {
		if b.Name == name {
			b.quotaExhausted.Store(exhausted)
			b.quotaLimit.Store(int64(limit * 100))
			b.quotaUsage.Store(int64(usage * 100))
			b.quotaCheckedAt.Store(time.Now().Unix())
			if exhausted {
				log.Printf("[backend:%s] quota exhausted: usage=%.2f limit=%.2f", b.Name, usage, limit)
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
