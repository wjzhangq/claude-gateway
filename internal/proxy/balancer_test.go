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
		{"forbidden-403", 403, nil, proxy.ErrForbidden},
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

// State constants mirrored from balancer.go for readable assertions.
// (The package-internal enum is unexported; these must match its values.)
const (
	stHealthy    = 0
	stDegraded   = 1
	stIsolated   = 3
	stProbing    = 4
	stQuarantine = 5
)

func TestStateMachine_NodeErrorIsolatesImmediately(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	if b == nil {
		t.Fatal("expected backend")
	}
	if b.State() != stHealthy {
		t.Fatalf("expected healthy(%d), got %d", stHealthy, b.State())
	}

	// A single node-level error (5xx) isolates within one request (SC-001).
	b.RecordResult(proxy.ErrServer)
	if b.State() != stIsolated {
		t.Fatalf("expected isolated(%d) after one 5xx, got %d", stIsolated, b.State())
	}
	// Isolated is NOT routable.
	if lb.Pick() != nil {
		t.Fatal("expected nil after backend isolated")
	}
}

func TestStateMachine_ConsecutiveFailuresQuarantine(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// 5 consecutive health-impacting failures escalate to Quarantine.
	for i := 0; i < 5; i++ {
		b.RecordResult(proxy.ErrServer)
	}
	if b.State() != stQuarantine {
		t.Fatalf("expected quarantine(%d) after 5 errors, got %d", stQuarantine, b.State())
	}
	if lb.Pick() != nil {
		t.Fatal("expected nil after backend quarantined")
	}
}

func TestStateMachine_ClientErrorIgnored(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// Many request-level 4xx (400/404/422) must NOT affect health (FR-008).
	for i := 0; i < 20; i++ {
		b.RecordResult(proxy.ErrClient)
	}
	if b.State() != stHealthy {
		t.Fatalf("client errors must not change state, got %d", b.State())
	}
}

func TestStateMachine_AuthQuarantines(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// 401 -> Quarantine (+ alert), not a transient isolation (FR-006).
	b.RecordResult(proxy.ErrAuth)
	if b.State() != stQuarantine {
		t.Fatalf("401 must quarantine, got %d", b.State())
	}
}

func TestStateMachine_ForbiddenIsolates(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// 403 -> Isolated (transient), separate from 401 quarantine (FR-005).
	b.RecordResult(proxy.ErrForbidden)
	if b.State() != stIsolated {
		t.Fatalf("403 must isolate (not quarantine), got %d", b.State())
	}
}

func TestSetHealthStatus_ProbeRecovery(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// Drive to Isolated with a single node-level error.
	b.RecordResult(proxy.ErrServer)
	if b.State() != stIsolated || lb.Pick() != nil {
		t.Fatalf("expected isolated and unselectable, got %d", b.State())
	}

	// Probe OK: Isolated -> Probing (routable again at weight 1).
	if !lb.SetHealthStatus("backend", true, 100) {
		t.Fatal("SetHealthStatus should find the backend")
	}
	if b.State() != stProbing {
		t.Fatalf("expected probing(%d) after probe OK, got %d", stProbing, b.State())
	}
	if lb.Pick() == nil {
		t.Fatal("expected selectable after probe recovery")
	}

	// Probe OK again: Probing -> Degraded.
	lb.SetHealthStatus("backend", true, 100)
	if b.State() != stDegraded {
		t.Fatalf("expected degraded(%d) after second probe OK, got %d", stDegraded, b.State())
	}

	// Probe OK a third time: Degraded -> Healthy.
	lb.SetHealthStatus("backend", true, 100)
	if b.State() != stHealthy {
		t.Fatalf("expected healthy(%d) after third probe OK, got %d", stHealthy, b.State())
	}
}

func TestSetHealthStatus_ProbeFailureDemotes(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	// healthy -> degraded
	lb.SetHealthStatus("backend", false, 0)
	if b.State() != stDegraded {
		t.Fatalf("expected degraded after probe fail, got %d", b.State())
	}
	// degraded -> isolated
	lb.SetHealthStatus("backend", false, 0)
	if b.State() != stIsolated {
		t.Fatalf("expected isolated after second probe fail, got %d", b.State())
	}
}

func TestSetHealthStatus_UnknownBackend(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	if lb.SetHealthStatus("nonexistent", true, 0) {
		t.Fatal("expected false for unknown backend")
	}
}
