package proxy

import (
	"encoding/json"
	"log"
	"math/rand"
	"strings"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

const maxStatusCodes = 50

// Backend represents a single upstream API endpoint with its HTTP client.
type Backend struct {
	Name            string
	URL             string
	APIKey          string
	Weight          int
	client          *http.Client
	errCount        atomic.Int64
	lastErr         atomic.Int64 // unix timestamp of last error
	disabled        atomic.Bool
	validationFailed atomic.Bool  // set on startup validation failure; never auto-recovered
	quotaExhausted  atomic.Bool   // set by external check when daily quota is exceeded
	quotaLimit      atomic.Int64  // hard_limit_usd * 100 (cents precision)
	quotaUsage      atomic.Int64  // total_usage * 100 (cents precision)
	quotaCheckedAt  atomic.Int64  // unix timestamp of last quota check
	statusCodes     []int         // last 50 status codes
	statusCodeDist  map[int]int   // distribution of status codes
	statusMu        sync.Mutex
}

// Client returns the backend's dedicated HTTP client.
func (b *Backend) Client() *http.Client { return b.client }

// RecordError increments the error counter and disables the backend after 5 consecutive errors.
func (b *Backend) RecordError() {
	b.errCount.Add(1)
	b.lastErr.Store(time.Now().Unix())
	if b.errCount.Load() >= 5 {
		b.disabled.Store(true)
	}
}

// RecordSuccess resets the error counter and re-enables the backend.
func (b *Backend) RecordSuccess() {
	b.errCount.Store(0)
	b.disabled.Store(false)
}

// RecordStatusCode records a status code for the last N requests tracking.
func (b *Backend) RecordStatusCode(code int) {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	b.statusCodes = append(b.statusCodes, code)
	if len(b.statusCodes) > maxStatusCodes {
		// Remove old code from distribution
		oldCode := b.statusCodes[0]
		if b.statusCodeDist[oldCode] > 0 {
			b.statusCodeDist[oldCode]--
			if b.statusCodeDist[oldCode] == 0 {
				delete(b.statusCodeDist, oldCode)
			}
		}
		b.statusCodes = b.statusCodes[1:]
	}
	// Add to distribution
	if b.statusCodeDist == nil {
		b.statusCodeDist = make(map[int]int)
	}
	b.statusCodeDist[code]++
}

// GetStatusCodes returns the last N status codes.
func (b *Backend) GetStatusCodes() []int {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	result := make([]int, len(b.statusCodes))
	copy(result, b.statusCodes)
	return result
}

// GetStatusCodeDist returns the distribution of status codes.
func (b *Backend) GetStatusCodeDist() map[int]int {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	result := make(map[int]int)
	for k, v := range b.statusCodeDist {
		result[k] = v
	}
	return result
}

// GetErrorRate returns the non-2xx error rate from the last N status codes.
func (b *Backend) GetErrorRate() float64 {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	if len(b.statusCodes) == 0 {
		return 0
	}
	non2xx := 0
	for _, code := range b.statusCodes {
		if code < 200 || code >= 300 {
			non2xx++
		}
	}
	return float64(non2xx) / float64(len(b.statusCodes)) * 100
}

// BackendInfo represents backend status info for API responses.
type BackendInfo struct {
	Name            string      `json:"name"`
	URL             string      `json:"url"`
	Weight          int         `json:"weight"`
	Disabled        bool        `json:"disabled"`
	ErrCount        int64       `json:"err_count"`
	StatusCodeDist  map[int]int `json:"status_code_dist"`
	StatusCodes     []int       `json:"status_codes"`
	ErrorRate       float64     `json:"error_rate"`
	QuotaExhausted  bool        `json:"quota_exhausted"`
	QuotaLimit      float64     `json:"quota_limit"`
	QuotaUsage      float64     `json:"quota_usage"`
	QuotaCheckedAt  int64       `json:"quota_checked_at"`
}

// GetBackends returns all backends with their status info.
func (lb *LoadBalancer) GetBackends() []BackendInfo {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	result := make([]BackendInfo, 0, len(lb.backends))
	for _, b := range lb.backends {
		result = append(result, BackendInfo{
			Name:           b.Name,
			URL:            b.URL,
			Weight:         b.Weight,
			Disabled:       b.disabled.Load(),
			ErrCount:       b.errCount.Load(),
			StatusCodeDist: b.GetStatusCodeDist(),
			StatusCodes:    b.GetStatusCodes(),
			ErrorRate:      b.GetErrorRate(),
			QuotaExhausted: b.quotaExhausted.Load(),
			QuotaLimit:     float64(b.quotaLimit.Load()) / 100,
			QuotaUsage:     float64(b.quotaUsage.Load()) / 100,
			QuotaCheckedAt: b.quotaCheckedAt.Load(),
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
	go lb.recoveryLoop()
	go lb.quotaResetLoop()
	return lb
}

// Pick selects a healthy backend using weighted random selection.
// Returns nil if no healthy backend is available.
// Uses two-pass scan to avoid per-call slice allocation.
func (lb *LoadBalancer) Pick() *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// First pass: compute total weight of healthy backends
	totalWeight := 0
	for _, b := range lb.backends {
		if !b.disabled.Load() && !b.validationFailed.Load() && !b.quotaExhausted.Load() {
			totalWeight += b.Weight
		}
	}
	if totalWeight == 0 {
		return nil
	}

	// Second pass: weighted selection
	r := rand.Intn(totalWeight)
	for _, b := range lb.backends {
		if b.disabled.Load() || b.validationFailed.Load() || b.quotaExhausted.Load() {
			continue
		}
		r -= b.Weight
		if r < 0 {
			return b
		}
	}
	// Fallback: return last healthy backend (handles rounding edge cases)
	for i := len(lb.backends) - 1; i >= 0; i-- {
		b := lb.backends[i]
		if !b.disabled.Load() && !b.validationFailed.Load() && !b.quotaExhausted.Load() {
			return b
		}
	}
	return nil
}

// recoveryLoop re-enables backends that have been quiet for 30 seconds.
func (lb *LoadBalancer) recoveryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now().Unix()
		lb.mu.RLock()
		for _, b := range lb.backends {
			if b.disabled.Load() && !b.validationFailed.Load() && now-b.lastErr.Load() > 30 {
				b.RecordSuccess()
			}
		}
		lb.mu.RUnlock()
	}
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
