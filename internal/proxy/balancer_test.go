package proxy_test

import (
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

func TestBackend_DisableAfterErrors(t *testing.T) {
	lb := proxy.NewLoadBalancer(makeBackends(10))
	b := lb.Pick()
	if b == nil {
		t.Fatal("expected backend")
	}

	// Record 5 errors to trigger disable
	for i := 0; i < 5; i++ {
		b.RecordError()
	}

	// Backend should now be disabled
	if lb.Pick() != nil {
		t.Fatal("expected nil after backend disabled")
	}

	// RecordSuccess should re-enable
	b.RecordSuccess()
	if lb.Pick() == nil {
		t.Fatal("expected backend after recovery")
	}
}
