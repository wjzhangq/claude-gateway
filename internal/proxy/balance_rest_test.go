package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBalanceProbe_SuccessReturnsHealthy(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Errorf("probe used %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"p"}]}`)
	}))
	defer srv.Close()

	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.URL = srv.URL
	b.APIKey = "k"
	lb.enterBalanceRest(b)
	client := &http.Client{Timeout: 5 * time.Second}
	now := time.Unix(*frozen, 0)

	lb.runActiveProbes(client, now, true)
	if hits != 0 {
		t.Fatalf("probed during the rest window: hits=%d", hits)
	}
	if b.State() != stateIsolated || b.effectiveWeight() != 0 {
		t.Fatal("account must stay unselectable during rest")
	}

	advance(frozen, balanceRestSecs)
	lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
	if hits != 1 {
		t.Fatalf("expected one messages probe, hits=%d", hits)
	}
	if b.balanceHold.Load() || b.State() != stateHealthy || b.effectiveWeight() <= 0 {
		t.Fatalf("probe success must return the account to normal: hold=%v state=%d weight=%d", b.balanceHold.Load(), b.State(), b.effectiveWeight())
	}
}

func TestBalanceProbe_FailureExtendsRest(t *testing.T) {
	var modelsHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			modelsHits++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"用户额度不足"}}`)
	}))
	defer srv.Close()

	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.URL = srv.URL
	b.APIKey = "k"
	lb.enterBalanceRest(b)
	advance(frozen, balanceRestSecs)
	now := time.Unix(*frozen, 0)

	lb.runActiveProbes(&http.Client{Timeout: 5 * time.Second}, now, true)
	if modelsHits != 0 {
		t.Fatal("a balance probe must not use GET /v1/models")
	}
	if !b.balanceHold.Load() || b.State() != stateIsolated || b.effectiveWeight() != 0 {
		t.Fatalf("failed probe must keep the account out: hold=%v state=%d weight=%d", b.balanceHold.Load(), b.State(), b.effectiveWeight())
	}
	if got, want := b.ttlUntil.Load(), now.Unix()+balanceRestSecs; got != want {
		t.Fatalf("rest timer = %d, want %d", got, want)
	}
}

func TestBalanceRest_ReleaseAndFailsafeSkip(t *testing.T) {
	lb, frozen := newTestLB(multiCfg(5, 10))
	for _, b := range lb.backends {
		lb.enterBalanceRest(b)
	}
	advance(frozen, balanceRestSecs+5)
	now := time.Unix(*frozen, 0)
	lb.releaseExpired(now)
	lb.failsafe(now)
	for _, b := range lb.backends {
		if b.State() != stateIsolated || !b.balanceHold.Load() || b.effectiveWeight() != 0 {
			t.Fatalf("%s was handed back to users: state=%d hold=%v weight=%d", b.Name, b.State(), b.balanceHold.Load(), b.effectiveWeight())
		}
	}
	if lb.SetHealthStatusDetailed(lb.backends[0].Name, true, 1, 200, 0) != true {
		t.Fatal("health sync should still find the backend")
	}
	if lb.backends[0].State() != stateIsolated || !lb.backends[0].balanceHold.Load() {
		t.Fatal("manual health sync must not end a balance rest")
	}
	infos := lb.GetBackends()
	if len(infos) != 5 || !infos[0].BalanceRest || infos[0].State != "isolated" {
		t.Fatalf("status API must mark balance rest: %+v", infos[0])
	}
}
