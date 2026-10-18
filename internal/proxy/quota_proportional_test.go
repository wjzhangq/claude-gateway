package proxy

import (
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

// TestQuotaProportionalWeights verifies that backends are initialized with
// weights proportional to their daily_usd quotas, ensuring traffic distribution
// matches quota allocation and all backends exhaust at roughly the same rate.
func TestQuotaProportionalWeights(t *testing.T) {
	cfg := &config.Config{
		BackendDailyLimits: []config.BackendDailyLimit{
			{Prefix: "lianxiang-", DailyUSD: 5000},
			{Prefix: "dasheng-", DailyUSD: 400},
			{Name: "special-backend", DailyUSD: 1000},
		},
	}

	backendCfgs := []config.BackendAPI{
		{Name: "lianxiang-sc031", URL: "http://example.com", APIKey: "key1", Weight: 100, Enabled: true},
		{Name: "lianxiang-sc032", URL: "http://example.com", APIKey: "key2", Weight: 100, Enabled: true},
		{Name: "dasheng-01", URL: "http://example.com", APIKey: "key3", Weight: 100, Enabled: true},
		{Name: "dasheng-02", URL: "http://example.com", APIKey: "key4", Weight: 100, Enabled: true},
		{Name: "special-backend", URL: "http://example.com", APIKey: "key5", Weight: 50, Enabled: true},
		{Name: "no-quota-backend", URL: "http://example.com", APIKey: "key6", Weight: 200, Enabled: true},
	}

	lb := NewLoadBalancerWithConfig(backendCfgs, cfg)

	// Verify weights are set according to daily_usd quotas
	expected := map[string]int{
		"lianxiang-sc031":  5000, // prefix match: lianxiang- → 5000
		"lianxiang-sc032":  5000,
		"dasheng-01":       400, // prefix match: dasheng- → 400
		"dasheng-02":       400,
		"special-backend":  1000, // exact name match → 1000
		"no-quota-backend": 200,  // no quota configured → use config weight
	}

	lb.mu.RLock()
	defer lb.mu.RUnlock()

	for _, b := range lb.backends {
		want := expected[b.Name]
		if b.Weight != want {
			t.Errorf("backend %s: weight = %d, want %d", b.Name, b.Weight, want)
		}
	}

	// Verify traffic distribution ratio matches quota ratio
	// lianxiang total: 5000 × 2 = 10000
	// dasheng total: 400 × 2 = 800
	// special: 1000
	// no-quota: 200
	// Expected ratio: 10000:800:1000:200 = 50:4:5:1

	lianxiangWeight := 0
	dashengWeight := 0
	for _, b := range lb.backends {
		if b.Name == "lianxiang-sc031" || b.Name == "lianxiang-sc032" {
			lianxiangWeight += b.Weight
		}
		if b.Name == "dasheng-01" || b.Name == "dasheng-02" {
			dashengWeight += b.Weight
		}
	}

	ratio := float64(lianxiangWeight) / float64(dashengWeight)
	expectedRatio := 10000.0 / 800.0 // 12.5
	if ratio != expectedRatio {
		t.Errorf("lianxiang:dasheng weight ratio = %.2f, want %.2f", ratio, expectedRatio)
	}
}

// TestQuotaProportionalFallback verifies that backends without quota config
// fall back to using their configured weight.
func TestQuotaProportionalFallback(t *testing.T) {
	cfg := &config.Config{
		BackendDailyLimits: []config.BackendDailyLimit{
			{Name: "backend-a", DailyUSD: 1000},
		},
	}

	backendCfgs := []config.BackendAPI{
		{Name: "backend-a", URL: "http://example.com", APIKey: "key1", Weight: 50, Enabled: true},
		{Name: "backend-b", URL: "http://example.com", APIKey: "key2", Weight: 200, Enabled: true},
		{Name: "backend-c", URL: "http://example.com", APIKey: "key3", Weight: 0, Enabled: true},
	}

	lb := NewLoadBalancerWithConfig(backendCfgs, cfg)

	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// backend-a: has quota → weight = 1000
	if lb.backends[0].Weight != 1000 {
		t.Errorf("backend-a: weight = %d, want 1000 (from quota)", lb.backends[0].Weight)
	}

	// backend-b: no quota → use config weight 200
	if lb.backends[1].Weight != 200 {
		t.Errorf("backend-b: weight = %d, want 200 (from config)", lb.backends[1].Weight)
	}

	// backend-c: no quota, no config weight → default 100
	if lb.backends[2].Weight != 100 {
		t.Errorf("backend-c: weight = %d, want 100 (default)", lb.backends[2].Weight)
	}
}

// TestBackwardCompatibility verifies that NewLoadBalancer (without config)
// still works and uses config weights.
func TestBackwardCompatibility(t *testing.T) {
	backendCfgs := []config.BackendAPI{
		{Name: "backend-1", URL: "http://example.com", APIKey: "key1", Weight: 100, Enabled: true},
		{Name: "backend-2", URL: "http://example.com", APIKey: "key2", Weight: 200, Enabled: true},
	}

	lb := NewLoadBalancer(backendCfgs)

	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// Without config, should use original config weights
	if lb.backends[0].Weight != 100 {
		t.Errorf("backend-1: weight = %d, want 100", lb.backends[0].Weight)
	}
	if lb.backends[1].Weight != 200 {
		t.Errorf("backend-2: weight = %d, want 200", lb.backends[1].Weight)
	}
}
