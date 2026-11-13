package proxy

import (
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

func dynCfg(name, url string) config.BackendAPI {
	return config.BackendAPI{Name: name, URL: url, APIKey: "k", Weight: 10, Enabled: true}
}

func TestAddBackendAppendsAndRoutes(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))

	if err := lb.AddBackend(dynCfg("added", "https://added.example.invalid")); err != nil {
		t.Fatalf("AddBackend: %v", err)
	}

	lb.mu.RLock()
	count := len(lb.backends)
	lb.mu.RUnlock()
	if count != 2 {
		t.Fatalf("got %d backends, want 2", count)
	}

	// A fresh backend must be routable, or adding a key would have no effect
	// until some other event promoted it.
	var added *Backend
	lb.mu.RLock()
	for _, b := range lb.backends {
		if b.Name == "added" {
			added = b
		}
	}
	lb.mu.RUnlock()
	if added == nil {
		t.Fatal("added backend not found")
	}
	if !isRoutable(added.state.Load()) {
		t.Errorf("added backend state = %d, want a routable state", added.state.Load())
	}
	if added.effectiveWeight() == 0 {
		t.Error("added backend has zero effective weight; it will never be picked")
	}
}

// Names index health state and per-backend quota, so a duplicate must be
// refused rather than creating two entries that share counters.
func TestAddBackendRejectsDuplicateName(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))

	err := lb.AddBackend(dynCfg("b", "https://other.example.invalid"))
	if err == nil {
		t.Fatal("adding a backend with an existing name was accepted")
	}

	lb.mu.RLock()
	count := len(lb.backends)
	lb.mu.RUnlock()
	if count != 1 {
		t.Errorf("rejected add still changed the fleet: %d backends, want 1", count)
	}
}

func TestAddBackendRejectsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.BackendAPI
	}{
		{"no name", config.BackendAPI{URL: "https://h.example.invalid", APIKey: "k"}},
		{"no url", config.BackendAPI{Name: "x", APIKey: "k"}},
		{"no api key", config.BackendAPI{Name: "x", URL: "https://h.example.invalid"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lb, _ := newTestLB(singleCfg(10))
			if err := lb.AddBackend(tt.cfg); err == nil {
				t.Errorf("%s was accepted", tt.name)
			}
		})
	}
}

// Parking a backend must take it out of rotation without removing it, so its
// counters survive and it can come back.
func TestSetBackendEnabledParksAndRestores(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]

	if err := lb.SetBackendEnabled("b", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if b.effectiveWeight() != 0 {
		t.Errorf("disabled backend has weight %d, want 0", b.effectiveWeight())
	}
	if isRoutable(b.state.Load()) {
		t.Error("disabled backend is still routable")
	}

	lb.mu.RLock()
	count := len(lb.backends)
	lb.mu.RUnlock()
	if count != 1 {
		t.Errorf("disable removed the backend; got %d entries, want 1", count)
	}

	if err := lb.SetBackendEnabled("b", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !isRoutable(b.state.Load()) {
		t.Error("re-enabled backend is not routable")
	}
	if b.effectiveWeight() == 0 {
		t.Error("re-enabled backend still has zero weight")
	}
}

// The park TTL has to outlast the reconcile pass, or releaseExpired would
// quietly promote a backend an operator took out.
func TestSetBackendEnabledParkSurvivesReconcile(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]

	if err := lb.SetBackendEnabled("b", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// Jump well past any normal isolation TTL (the longest is the 6h quota one).
	advance(frozen, 24*60*60)
	lb.releaseExpired(lb.nowFn())

	if isRoutable(b.state.Load()) {
		t.Error("a parked backend was promoted back by the reconcile pass")
	}
}

// Re-enabling is an operator asserting the key works again, so a balance hold
// must not keep it parked.
func TestSetBackendEnabledClearsBalanceHold(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]

	lb.enterBalanceRest(b)
	if !b.balanceHold.Load() {
		t.Fatal("fixture did not enter balance rest")
	}

	if err := lb.SetBackendEnabled("b", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if b.balanceHold.Load() {
		t.Error("balance hold survived an explicit re-enable")
	}
	if b.effectiveWeight() == 0 {
		t.Error("re-enabled backend still has zero weight")
	}
}

func TestSetBackendEnabledUnknownName(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	if err := lb.SetBackendEnabled("nonexistent", false); err == nil {
		t.Error("disabling an unknown backend reported success")
	}
}

// Capacity is sized from the fleet count, so adding a backend must resize the
// existing ring buffers too.
func TestAddBackendUpdatesCapacities(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))

	for i := 0; i < 12; i++ {
		cfg := dynCfg("n"+string(rune('a'+i)), "https://h.example.invalid")
		if err := lb.AddBackend(cfg); err != nil {
			t.Fatalf("AddBackend %d: %v", i, err)
		}
	}

	lb.mu.RLock()
	defer lb.mu.RUnlock()
	want := 10 * len(lb.backends)
	if want < minStatusCodes {
		want = minStatusCodes
	}
	for _, b := range lb.backends {
		b.statusMu.Lock()
		got := b.statusCapacity
		b.statusMu.Unlock()
		if got != want {
			t.Errorf("backend %s capacity = %d, want %d", b.Name, got, want)
		}
	}
}

// Backends added at runtime should share the per-host pool, not open their own.
func TestAddBackendSharesTransport(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	const host = "https://pool.example.invalid"

	if err := lb.AddBackend(dynCfg("p1", host)); err != nil {
		t.Fatalf("AddBackend p1: %v", err)
	}
	if err := lb.AddBackend(dynCfg("p2", host)); err != nil {
		t.Fatalf("AddBackend p2: %v", err)
	}

	lb.mu.RLock()
	defer lb.mu.RUnlock()
	var p1, p2 *Backend
	for _, b := range lb.backends {
		switch b.Name {
		case "p1":
			p1 = b
		case "p2":
			p2 = b
		}
	}
	if p1 == nil || p2 == nil {
		t.Fatal("added backends not found")
	}
	if p1.Client().Transport != p2.Client().Transport {
		t.Error("backends added at runtime on one host do not share a transport")
	}
}
