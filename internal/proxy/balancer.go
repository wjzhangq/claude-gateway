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
	return lb
}

// Pick selects a healthy backend using weighted random selection.
// Returns nil if no healthy backend is available.
func (lb *LoadBalancer) Pick() *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var pool []*Backend
	totalWeight := 0
	for _, b := range lb.backends {
		if !b.disabled.Load() && !b.validationFailed.Load() {
			pool = append(pool, b)
			totalWeight += b.Weight
		}
	}
	if len(pool) == 0 {
		return nil
	}

	r := rand.Intn(totalWeight)
	for _, b := range pool {
		r -= b.Weight
		if r < 0 {
			return b
		}
	}
	return pool[len(pool)-1]
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
