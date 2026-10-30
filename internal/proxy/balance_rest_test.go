package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if got, want := b.ttlUntil.Load(), now.Unix()+balanceRestBackoff(1); got != want {
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

// restFixture puts a backend into balance rest against an upstream that always
// refuses for insufficient balance, which is what a negative account does.
func restFixture(t *testing.T) (*LoadBalancer, *Backend, *int64, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"用户额度不足, 剩余额度: $-3"}}`)
	}))
	t.Cleanup(srv.Close)

	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.URL = srv.URL
	b.APIKey = "k"
	return lb, b, frozen, &http.Client{Timeout: 5 * time.Second}
}

// A negative balance needs a human to fix it. Probing every minute forever
// spends a billable inference call per minute, so the rest window doubles.
func TestBalanceRest_BackoffGrowsAndCaps(t *testing.T) {
	lb, b, frozen, client := restFixture(t)

	lb.enterBalanceRest(b)
	if got, want := b.ttlUntil.Load()-*frozen, int64(balanceRestSecs); got != want {
		t.Fatalf("first rest = %ds, want %ds", got, want)
	}

	for _, want := range []int64{120, 240, 480, 960, balanceRestMaxSecs, balanceRestMaxSecs} {
		advance(frozen, b.ttlUntil.Load()-*frozen)
		lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
		if !b.balanceHold.Load() || b.effectiveWeight() != 0 {
			t.Fatalf("a failed probe handed the account back: hold=%v weight=%d",
				b.balanceHold.Load(), b.effectiveWeight())
		}
		if got := b.ttlUntil.Load() - *frozen; got != want {
			t.Fatalf("rest window = %ds, want %ds", got, want)
		}
	}
}

// Sustained failure is an operational event: somebody has to top the account up.
func TestBalanceRest_SustainedFailureAlerts(t *testing.T) {
	lb, b, frozen, client := restFixture(t)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	lb.enterBalanceRest(b)
	for i := 0; i < balanceAlertRounds-1; i++ {
		advance(frozen, b.ttlUntil.Load()-*frozen)
		lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
	}
	if strings.Contains(buf.String(), "ALERT") {
		t.Fatalf("alerted before %d rounds:\n%s", balanceAlertRounds, buf.String())
	}

	advance(frozen, b.ttlUntil.Load()-*frozen)
	lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
	if !strings.Contains(buf.String(), "ALERT balance") {
		t.Fatalf("no balance alert after %d rounds:\n%s", balanceAlertRounds, buf.String())
	}
}

// A top-up clears the backoff: the next incident starts from one minute again.
func TestBalanceRest_SuccessResetsBackoff(t *testing.T) {
	var paid bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !paid {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"用户额度不足"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"p"}]}`)
	}))
	defer srv.Close()

	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.URL = srv.URL
	b.APIKey = "k"
	client := &http.Client{Timeout: 5 * time.Second}

	lb.enterBalanceRest(b)
	for i := 0; i < 3; i++ {
		advance(frozen, b.ttlUntil.Load()-*frozen)
		lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
	}
	if b.balanceRestCount.Load() == 0 {
		t.Fatal("expected the backoff counter to have grown")
	}

	paid = true
	advance(frozen, b.ttlUntil.Load()-*frozen)
	lb.runActiveProbes(client, time.Unix(*frozen, 0), false)
	if b.balanceHold.Load() || b.State() != stateHealthy {
		t.Fatalf("a successful probe must end the rest: hold=%v state=%d", b.balanceHold.Load(), b.State())
	}
	if got := b.balanceRestCount.Load(); got != 0 {
		t.Fatalf("backoff counter = %d after recovery, want 0", got)
	}

	lb.enterBalanceRest(b)
	if got, want := b.ttlUntil.Load()-*frozen, int64(balanceRestSecs); got != want {
		t.Fatalf("rest after recovery = %ds, want %ds", got, want)
	}
}

// A revoked key does not self-heal, so setQuarantine backs the retry off
// exponentially. The general probe pass has to honor that timer or the backoff
// is decorative: a quarantined account would still be probed every minute.
func TestActiveProbes_HonorQuarantineBackoff(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.URL = srv.URL
	b.APIKey = "k"
	client := &http.Client{Timeout: 5 * time.Second}

	b.RecordRequest(http.StatusUnauthorized, 1)
	b.RecordResult(ErrAuth)
	if b.State() != stateQuarantine {
		t.Fatalf("state = %d, want quarantine", b.State())
	}
	ttl := b.ttlUntil.Load()

	advance(frozen, 60)
	lb.runActiveProbes(client, time.Unix(*frozen, 0), true)
	if hits != 0 {
		t.Fatalf("probed a quarantined account %d time(s) inside its backoff window", hits)
	}

	advance(frozen, ttl-*frozen)
	lb.runActiveProbes(client, time.Unix(*frozen, 0), true)
	if hits == 0 {
		t.Fatal("the account was never retried after its backoff expired")
	}
}
