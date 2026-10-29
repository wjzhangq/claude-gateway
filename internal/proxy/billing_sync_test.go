package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

func TestSyncBilling_SuccessUpdatesNumbersOnly(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/dashboard/billing/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_usage":12.5}`))
		case "/v1/dashboard/billing/subscription":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hard_limit_usd":20}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	lb, _ := newTestLB([]config.BackendAPI{{
		Name: "acct", URL: srv.URL, APIKey: "k", Weight: 1, Enabled: true,
	}})
	b := lb.backends[0]
	b.setState(stateIsolated, lb.nowFn())
	b.quotaExhausted.Store(true)

	lb.syncBilling(srv.Client())

	if got := b.quotaUsage.Load(); got != 1250 {
		t.Fatalf("usage cents = %d, want 1250", got)
	}
	if got := b.quotaLimit.Load(); got != 2000 {
		t.Fatalf("limit cents = %d, want 2000", got)
	}
	if b.quotaCheckedAt.Load() == 0 {
		t.Fatal("quota_checked_at was not set")
	}
	if !b.quotaExhausted.Load() {
		t.Fatal("billing sync must not clear quotaExhausted")
	}
	if b.state.Load() != stateIsolated {
		t.Fatalf("state = %d, want isolated", b.state.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/v1/models" || p == "/v1/messages" {
			t.Fatalf("billing sync requested %s", p)
		}
	}
}

func TestSyncBilling_FailureLeavesNumbersAndSkipsModels(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.Error(w, "billing down", http.StatusBadGateway)
	}))
	defer srv.Close()

	lb, _ := newTestLB([]config.BackendAPI{{
		Name: "acct", URL: srv.URL, APIKey: "k", Weight: 1, Enabled: true,
	}})
	b := lb.backends[0]
	b.quotaLimit.Store(5000)
	b.quotaUsage.Store(1000)
	b.quotaCheckedAt.Store(42)
	stateBefore := b.state.Load()

	lb.syncBilling(srv.Client())

	if b.quotaLimit.Load() != 5000 || b.quotaUsage.Load() != 1000 || b.quotaCheckedAt.Load() != 42 {
		t.Fatalf("billing failure mutated numbers: limit=%d usage=%d checked=%d",
			b.quotaLimit.Load(), b.quotaUsage.Load(), b.quotaCheckedAt.Load())
	}
	if b.quotaExhausted.Load() {
		t.Fatal("billing failure must not set quotaExhausted")
	}
	if b.state.Load() != stateBefore {
		t.Fatalf("state changed from %d to %d", stateBefore, b.state.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/v1/models" || p == "/v1/messages" {
			t.Fatalf("billing failure fell back to %s", p)
		}
	}
}

func TestSetQuotaStatus_NumbersDoNotGate(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(1))
	b := lb.backends[0]
	if !lb.SetQuotaStatus("b", true, 10, 10) {
		t.Fatal("expected backend to be found")
	}
	if b.quotaExhausted.Load() {
		t.Fatal("billing report must not set quotaExhausted")
	}
	if b.quotaLimit.Load() != 1000 || b.quotaUsage.Load() != 1000 {
		t.Fatalf("numbers = %d/%d", b.quotaUsage.Load(), b.quotaLimit.Load())
	}
	if b.quotaCheckedAt.Load() != *frozen {
		t.Fatalf("checked_at = %d", b.quotaCheckedAt.Load())
	}
	if b.state.Load() != stateHealthy {
		t.Fatalf("state = %d", b.state.Load())
	}
}

func TestSetQuotaStatus_EmptyReportIsIgnored(t *testing.T) {
	lb, _ := newTestLB(singleCfg(1))
	b := lb.backends[0]
	b.quotaLimit.Store(800)
	b.quotaUsage.Store(100)
	b.quotaCheckedAt.Store(7)
	if !lb.SetQuotaStatus("b", true, 0, 0) {
		t.Fatal("expected backend to be found")
	}
	if b.quotaExhausted.Load() {
		t.Fatal("empty report must not raise the hard gate")
	}
	if b.quotaLimit.Load() != 800 || b.quotaUsage.Load() != 100 || b.quotaCheckedAt.Load() != 7 {
		t.Fatal("empty report wiped a previous billing reading")
	}
}

func TestSetOperatorExhausted(t *testing.T) {
	lb, _ := newTestLB(singleCfg(1))
	b := lb.backends[0]
	b.quotaLimit.Store(800)
	b.quotaUsage.Store(100)
	if !lb.SetOperatorExhausted("b", true) {
		t.Fatal("expected backend to be found")
	}
	if !b.quotaExhausted.Load() {
		t.Fatal("operator disable must set the hard gate")
	}
	if b.quotaLimit.Load() != 800 || b.quotaUsage.Load() != 100 {
		t.Fatal("operator switch wiped billing numbers")
	}
	if !lb.SetOperatorExhausted("b", false) {
		t.Fatal("expected backend to be found")
	}
	if b.quotaExhausted.Load() {
		t.Fatal("operator enable must clear the hard gate")
	}
}
