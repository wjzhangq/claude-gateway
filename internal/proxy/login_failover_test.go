package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

func TestIsLoginQuotaBody_CaseAndBreadth(t *testing.T) {
	if !isLoginQuotaBody([]byte(`Please run /LOGIN`)) {
		t.Fatal("/login must match case-insensitively")
	}
	if !isLoginQuotaBody([]byte(`预扣费额度失败, 用户剩余额度: 0`)) {
		t.Fatal("pre-charge body must match")
	}
	if isLoginQuotaBody([]byte(`{"error":{"message":"API Error: 403"}}`)) {
		t.Fatal("API Error: 403 alone must not count as a login prompt")
	}
	if isLoginQuotaBody([]byte(`{"error":{"message":"forbidden"}}`)) {
		t.Fatal("a plain permission error must not count as a login prompt")
	}
}

func TestDoRequestWithFailover_LoginSwitchesWithoutQuotaGate(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"Please run /login · 预扣费额度失败"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}]}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	c := failoverGinContext()
	c.Request.Header.Set("Accept-Encoding", "gzip")
	gotBE, resp, err, quotaFinal := h.doRequestWithFailover(c, badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if quotaFinal || resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("status=%d backend=%s quotaFinal=%v", resp.StatusCode, gotBE.Name, quotaFinal)
	}
	if badBE.quotaExhausted.Load() {
		t.Fatal("a login prompt must not take the quota hard-gate")
	}
	if badBE.State() != stateIsolated {
		t.Fatalf("login prompt must short-isolate, state=%d", badBE.State())
	}
	if got := c.Request.Header.Get("Accept-Encoding"); got != "gzip" {
		t.Fatal("client Accept-Encoding should stay on the incoming request")
	}
}

func TestDoRequestWithFailover_GzipLoginIsVisible(t *testing.T) {
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	_, _ = zw.Write([]byte(`{"error":{"message":"Please run /login"}}`))
	_ = zw.Close()
	payload := append([]byte(nil), body.Bytes()...)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(payload)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message"}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err, _ := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("gzip login prompt was not failed over: status=%d backend=%s", resp.StatusCode, gotBE.Name)
	}
}

func TestDoRequestWithFailover_AuthSwitchesWithoutQuotaGate(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"message":"invalid x-api-key"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message"}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err, quotaFinal := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if quotaFinal || resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("401 did not fail over: status=%d backend=%s", resp.StatusCode, gotBE.Name)
	}
	if badBE.quotaExhausted.Load() {
		t.Fatal("401 must not take the quota hard-gate")
	}
	if badBE.State() != stateQuarantine {
		t.Fatalf("401 must quarantine, state=%d", badBE.State())
	}
}

func TestIsBalanceBody(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"用户额度不足"}}`,
		`额度不足`,
		`余额不足`,
		`余额为负`,
		`预扣费额度失败, 用户剩余额度: 0`,
		`{"error":{"message":"insufficient balance"}}`,
	} {
		if !isBalanceBody([]byte(body)) {
			t.Fatalf("balance body must match: %s", body)
		}
	}
	if isBalanceBody([]byte(`{"type":"error","error":{"type":"permission_error","message":"API Error: 403"}}`)) {
		t.Fatal("a permission 403 must not count as a balance failure")
	}
}

func TestDoRequestWithFailover_BalanceRetriesOnce(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"用户额度不足"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message"}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err, quotaFinal := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if quotaFinal || resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("balance 403 did not fail over once: status=%d backend=%s quotaFinal=%v", resp.StatusCode, gotBE.Name, quotaFinal)
	}
	if badBE.quotaExhausted.Load() {
		t.Fatal("a balance 403 must not take the 6h quota gate")
	}
	if !badBE.balanceHold.Load() || badBE.State() != stateIsolated || badBE.effectiveWeight() != 0 {
		t.Fatalf("bad backend must rest unselectable: hold=%v state=%d weight=%d", badBE.balanceHold.Load(), badBE.State(), badBE.effectiveWeight())
	}
}

func TestDoRequestWithFailover_BalanceStopsAfterOneRetry(t *testing.T) {
	var secondHits, thirdHits int
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"用户额度不足"}}`)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits++
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"余额为负"}}`)
	}))
	defer second.Close()
	third := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		thirdHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer third.Close()

	lb := &LoadBalancer{nowFn: time.Now, rng: rand.New(rand.NewSource(1))}
	a := lb.newBackend(config.BackendAPI{Name: "a", URL: first.URL, APIKey: "k", Weight: 1, Enabled: true})
	b := lb.newBackend(config.BackendAPI{Name: "b", URL: second.URL, APIKey: "k", Weight: 100, Enabled: true})
	c := lb.newBackend(config.BackendAPI{Name: "c", URL: third.URL, APIKey: "k", Weight: 1, Enabled: true})
	lb.backends = []*Backend{a, b, c}
	lb.updateCapacities()
	h := NewHandler(lb, nil, nil, &config.Config{}, nil)

	gotBE, resp, err, _ := h.doRequestWithFailover(failoverGinContext(), a, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || gotBE.Name != "b" || secondHits != 1 || thirdHits != 0 {
		t.Fatalf("must stop after one retry: status=%d backend=%s second=%d third=%d body=%s", resp.StatusCode, gotBE.Name, secondHits, thirdHits, body)
	}
	if !a.balanceHold.Load() || !b.balanceHold.Load() || c.balanceHold.Load() {
		t.Fatalf("only the two tried accounts rest: a=%v b=%v c=%v", a.balanceHold.Load(), b.balanceHold.Load(), c.balanceHold.Load())
	}
}

func TestDoRequestWithFailover_Plain403Stays(t *testing.T) {
	var goodHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"API Error: 403"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err, _ := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || gotBE.Name != "bad" || goodHits != 0 {
		t.Fatalf("plain 403 must stay: status=%d backend=%s hits=%d body=%s", resp.StatusCode, gotBE.Name, goodHits, body)
	}
	if !strings.Contains(string(body), "permission_error") {
		t.Fatalf("body was not preserved: %s", body)
	}
}

func TestDoRequestWithFailover_QuotaSetsGate(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"exceeded_current_quota_error","message":"insufficient balance"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message"}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err, quotaFinal := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if quotaFinal || resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("quota 429 did not fail over: status=%d backend=%s quotaFinal=%v", resp.StatusCode, gotBE.Name, quotaFinal)
	}
	if !badBE.quotaExhausted.Load() {
		t.Fatal("confirmed balance failure must set the quota gate")
	}
	// Health accounting writes the 30s 429 TTL first. The quota gate must run
	// after that and leave the long cooldown in place.
	if badBE.TTLUntil() < time.Now().Add(time.Hour).Unix() {
		t.Fatalf("quota TTL was overwritten by the short 429 base, until=%d", badBE.TTLUntil())
	}
}

func TestRunWebSearchLoop_FailoversLoginOnFirstRound(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"Please run /login"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	req, err := websearch.DecodeRequest([]byte(`{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	res, lerr := h.runWebSearchLoop(failoverGinContext(), badBE, "/v1/messages", "claude-opus-4-8", req, nil, config.WebSearchConfig{}, time.Now(), nil)
	if lerr != nil {
		t.Fatalf("loop error: %+v", lerr)
	}
	if res.BackendName != "good" {
		t.Fatalf("backend = %s, want good", res.BackendName)
	}
	if badBE.quotaExhausted.Load() {
		t.Fatal("websearch login failover must not set the quota gate")
	}
}

func TestReleaseExpired_ClearsQuotaGate(t *testing.T) {
	lb := &LoadBalancer{nowFn: time.Now, rng: rand.New(rand.NewSource(1))}
	b := lb.newBackend(config.BackendAPI{Name: "b", URL: "http://b", APIKey: "k", Weight: 1, Enabled: true})
	lb.backends = []*Backend{b}
	lb.updateCapacities()
	now := time.Now()
	b.state.Store(stateIsolated)
	b.quotaExhausted.Store(true)
	b.ttlUntil.Store(now.Add(-time.Second).Unix())
	lb.releaseExpired(now)
	if b.quotaExhausted.Load() {
		t.Fatal("TTL expiry must clear the quota gate")
	}
	if b.State() != stateProbing {
		t.Fatalf("TTL expiry must enter probing, state=%d", b.State())
	}
	if b.effectiveWeight() == 0 {
		t.Fatal("a probed node must be selectable again")
	}
}

func failoverHandler(t *testing.T, badURL, goodURL string) (*Handler, *Backend) {
	t.Helper()
	lb := &LoadBalancer{
		nowFn: time.Now,
		rng:   rand.New(rand.NewSource(1)),
	}
	badBE := lb.newBackend(config.BackendAPI{Name: "bad", URL: badURL, APIKey: "k", Weight: 10, Enabled: true})
	goodBE := lb.newBackend(config.BackendAPI{Name: "good", URL: goodURL, APIKey: "k", Weight: 10, Enabled: true})
	lb.backends = []*Backend{badBE, goodBE}
	lb.updateCapacities()
	return NewHandler(lb, nil, nil, &config.Config{}, nil), badBE
}

func failoverGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}
