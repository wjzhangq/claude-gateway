package proxy

import (
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

// Backend represents a single upstream API endpoint with its HTTP client.
type Backend struct {
	Name       string
	URL        string
	APIKey     string
	Weight     int
	client     *http.Client
	errCount   atomic.Int64
	lastErr    atomic.Int64 // unix timestamp of last error
	disabled   atomic.Bool
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
		if !b.disabled.Load() {
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
			if b.disabled.Load() && now-b.lastErr.Load() > 30 {
				b.RecordSuccess()
			}
		}
		lb.mu.RUnlock()
	}
}
