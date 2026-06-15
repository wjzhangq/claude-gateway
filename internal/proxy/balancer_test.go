package proxy_test

import (
	"errors"
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/proxy"
)

func makeBackends(weights ...int) []config.BackendAPI {
	cfgs := make([]config.BackendAPI, len(weights))
	for i, w := range weights {
		cfgs[i] = config.BackendAPI{
			Name:    "backend",
			URL:     "http://localhost",
			APIKey:  "test",
			Weight:  w,
			Enabled: true,
		}
	}
	return cfgs
}

func TestLoadBalancer_Pick_SingleBackend(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	if b == nil {
		t.Fatal("expected backend, got nil")
	}
}

func TestLoadBalancer_Pick_NoBackends(t *testing.T) {
	lb := proxy.NewLoadBalancer(nil)
	if lb.Pick() != nil {
		t.Fatal("expected nil for empty backend list")
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		err    error
		expect proxy.ErrorClass
	}{
		{"success", 200, nil, proxy.ErrNone},
		{"created", 201, nil, proxy.ErrNone},
		{"client-400", 400, nil, proxy.ErrClient},
		{"client-404", 404, nil, proxy.ErrClient},
		{"auth-401", 401, nil, proxy.ErrAuth},
		{"auth-403", 403, nil, proxy.ErrAuth},
		{"ratelimit-429", 429, nil, proxy.ErrRateLimit},
		{"server-500", 500, nil, proxy.ErrServer},
		{"server-502", 502, nil, proxy.ErrServer},
		{"transport", 0, errors.New("connection refused"), proxy.ErrTransport},
		{"transport-overrides-code", 200, errors.New("boom"), proxy.ErrTransport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxy.ClassifyError(tc.code, tc.err); got != tc.expect {
				t.Fatalf("ClassifyError(%d, %v) = %v, want %v", tc.code, tc.err, got, tc.expect)
			}
		})
	}
}

func TestStateMachine_HealthyToDegradedToDisabled(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	if b == nil {
		t.Fatal("expected backend")
	}
	if b.State() != 0 {
		t.Fatalf("expected healthy(0), got %d", b.State())
	}

	// 3 consecutive server errors -> degraded (state 1)
	for i := 0; i < 3; i++ {
		b.RecordResult(proxy.ErrServer)
	}
	if b.State() != 1 {
		t.Fatalf("expected degraded(1) after 3 errors, got %d", b.State())
	}
	// degraded is still selectable (reduced weight)
	if lb.Pick() == nil {
		t.Fatal("expected degraded backend to still be selectable")
	}

	// 2 more (total 5) -> disabled (state 2)
	b.RecordResult(proxy.ErrServer)
	b.RecordResult(proxy.ErrServer)
	if b.State() != 2 {
		t.Fatalf("expected disabled(2) after 5 errors, got %d", b.State())
	}
	if lb.Pick() != nil {
		t.Fatal("expected nil after backend disabled")
	}
}

func TestStateMachine_ClientErrorIgnored(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// Many 4xx client errors must NOT affect health.
	for i := 0; i < 20; i++ {
		b.RecordResult(proxy.ErrClient)
	}
	if b.State() != 0 {
		t.Fatalf("client errors must not change state, got %d", b.State())
	}
}

func TestStateMachine_AuthDisablesImmediately(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	b.RecordResult(proxy.ErrAuth)
	if b.State() != 2 {
		t.Fatalf("auth failure must disable immediately, got %d", b.State())
	}
}

func TestStateMachine_DegradedRecoversPassively(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// Degrade it
	for i := 0; i < 3; i++ {
		b.RecordResult(proxy.ErrServer)
	}
	if b.State() != 1 {
		t.Fatalf("expected degraded, got %d", b.State())
	}
	// 3 consecutive successes -> healthy
	for i := 0; i < 3; i++ {
		b.RecordResult(proxy.ErrNone)
	}
	if b.State() != 0 {
		t.Fatalf("expected healthy after 3 successes, got %d", b.State())
	}
}

func TestSetHealthStatus_ProbeRecovery(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// Drive to disabled
	for i := 0; i < 5; i++ {
		b.RecordResult(proxy.ErrServer)
	}
	if b.State() != 2 || lb.Pick() != nil {
		t.Fatal("expected disabled and unselectable")
	}

	// Probe OK: disabled -> degraded (selectable again at reduced weight)
	if !lb.SetHealthStatus("backend", true, 100) {
		t.Fatal("SetHealthStatus should find the backend")
	}
	if b.State() != 1 {
		t.Fatalf("expected degraded after probe OK, got %d", b.State())
	}
	if lb.Pick() == nil {
		t.Fatal("expected selectable after probe recovery")
	}

	// Probe OK again: degraded -> healthy
	lb.SetHealthStatus("backend", true, 100)
	if b.State() != 0 {
		t.Fatalf("expected healthy after second probe OK, got %d", b.State())
	}
}

func TestSetHealthStatus_ProbeFailureDegrades(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// healthy -> degraded
	lb.SetHealthStatus("backend", false, 0)
	if b.State() != 1 {
		t.Fatalf("expected degraded after probe fail, got %d", b.State())
	}
	// degraded -> disabled
	lb.SetHealthStatus("backend", false, 0)
	if b.State() != 2 {
		t.Fatalf("expected disabled after second probe fail, got %d", b.State())
	}
}

func TestSetHealthStatus_UnknownBackend(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	if lb.SetHealthStatus("nonexistent", true, 0) {
		t.Fatal("expected false for unknown backend")
	}
}
