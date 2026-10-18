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
)

// ---------------------------------------------------------------------------
// Quota gate recovery
// ---------------------------------------------------------------------------

// A quota-isolated node must come back once its cooldown elapses. quotaExhausted
// is a hard gate in effectiveWeight, so leaving it set would make the Probing
// promotion a no-op and keep the node dark until the next CST midnight.
func TestQuotaIsolation_RecoversAfterCooldown(t *testing.T) {
	lb, clock := newTestLB(multiCfg(2, 10))
	b := lb.backends[0]

	lb.MarkQuotaExhausted(b.Name)
	if b.effectiveWeight() != 0 {
		t.Fatal("quota-exhausted backend must be unselectable during its cooldown")
	}

	advance(clock, quotaIsolationTTL*2)
	lb.Reconcile(lb.nowFn())

	if got := b.State(); got != stateProbing {
		t.Fatalf("state = %s, want probing after the cooldown", stateName(got))
	}
	if b.effectiveWeight() == 0 {
		t.Fatal("cooldown elapsed but the quota gate still pins the node to weight 0")
	}
}

// A 2xx proves the account works again, whatever set the gate.
func TestQuotaExhausted_ClearedBySuccess(t *testing.T) {
	lb, _ := newTestLB(multiCfg(2, 10))
	b := lb.backends[0]

	lb.MarkQuotaExhausted(b.Name)
	b.SetState(stateHealthy)
	lb.recordResult(b, ErrNone, 0, false)

	if b.effectiveWeight() == 0 {
		t.Fatal("a successful request must clear the quota gate")
	}
}

// With every node reporting quota, the failsafe is the only thing between the
// fleet and a day of "no available backend".
func TestFailsafe_ReleasesFleetPinnedByQuotaGate(t *testing.T) {
	lb, _ := newTestLB(multiCfg(5, 10))
	for _, b := range lb.backends {
		lb.MarkQuotaExhausted(b.Name)
	}
	if lb.Pick() == nil {
		t.Fatal("failsafe must restore routability when every node is quota-isolated")
	}
}

// Same, for a gate set by the external quota probe rather than a request-path
// report: those nodes stay in a routable state, so they are only reachable as
// failsafe candidates through the weight-0 check.
func TestFailsafe_ReleasesHealthyNodesPinnedByQuotaGate(t *testing.T) {
	lb, _ := newTestLB(multiCfg(5, 10))
	for _, b := range lb.backends {
		lb.SetQuotaStatus(b.Name, true, 100, 100)
	}
	if lb.Pick() == nil {
		t.Fatal("failsafe must release healthy nodes pinned to weight 0 by the quota gate")
	}
}

// ---------------------------------------------------------------------------
// Upstream classification
// ---------------------------------------------------------------------------

func TestClassifyUpstreamResponse(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantRetry bool
		wantQuota bool
	}{
		{"success", 200, `{"ok":true}`, false, false},
		{"bad request", 400, `{"error":"missing model"}`, false, false},
		{"not found", 404, `{"error":"no such endpoint"}`, false, false},
		{"unprocessable", 422, `{"error":"bad schema"}`, false, false},
		{"auth", 401, `{"error":"invalid api key"}`, true, false},
		{"login 403", 403, `{"error":"预扣费额度失败，请先登录"}`, true, false},
		{"claude login 403", 403, `Please run /login · API Error: 403`, true, false},
		{"quota 403", 403, `{"error":"insufficient balance"}`, true, true},
		{"plain 403", 403, `{"error":"permission_error"}`, true, false},
		{"timeout", 408, ``, true, false},
		{"rate limit", 429, `{"error":"too many requests"}`, true, false},
		{"quota 429", 429, `{"error":{"type":"exceeded_current_quota_error"}}`, true, true},
		{"server", 503, `no available channel`, true, false},
		{"overloaded", 529, `overloaded`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			_, v := classifyUpstreamResponse(resp)
			if v.Retry != tc.wantRetry {
				t.Fatalf("retry = %v, want %v (reason=%q)", v.Retry, tc.wantRetry, v.Reason)
			}
			if v.Quota != tc.wantQuota {
				t.Fatalf("quota = %v, want %v", v.Quota, tc.wantQuota)
			}
		})
	}
}

// claude-cli sends Accept-Encoding: gzip, so the rejection body the gateway has
// to recognize usually arrives compressed.
func TestClassifyBlockBody_Gzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"error":{"message":"预扣费额度失败"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Content-Encoding": []string{"gzip"}}

	if got := classifyBlockBody(header, buf.Bytes()); got != blockSoftLogin {
		t.Fatalf("gzipped login body classified as %v, want blockSoftLogin", got)
	}
}

// A body longer than the classification peek must still reach the client whole.
func TestRestoreBody_KeepsFullBody(t *testing.T) {
	full := bytes.Repeat([]byte("x"), maxPeekBytes*2)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(full)),
	}

	peek, _ := peekBlockBody(resp)
	restoreBody(resp, peek)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("restored body is %d bytes, want %d", len(got), len(full))
	}
}

// ---------------------------------------------------------------------------
// Request-path failover
// ---------------------------------------------------------------------------

// failoverHandler builds a Handler over the given backends. The first config
// gets the highest weight so Pick deterministically starts there.
func failoverHandler(cfgs ...config.BackendAPI) *Handler {
	lb := &LoadBalancer{nowFn: time.Now, rng: rand.New(rand.NewSource(1))}
	for _, cfg := range cfgs {
		lb.backends = append(lb.backends, lb.newBackend(cfg))
	}
	lb.updateCapacities()
	return NewHandler(lb, nil, nil, &config.Config{}, nil)
}

// errorUpstream serves a fixed status and body, counting calls.
func errorUpstream(status int, body string, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func forwardRequest(h *Handler, ua string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5-20250929","max_tokens":16}`))
	c.Request.Header.Set("Content-Type", "application/json")
	if ua != "" {
		c.Request.Header.Set("User-Agent", ua)
	}
	h.forward(c, "/v1/messages")
	return w
}

const claudeCodeUA = "claude-cli/2.1.0 (external, cli)"

// The reported symptom: a node starts answering with a login prompt. The client
// must land on the next node instead of being told to re-authenticate.
func TestForward_FailsOverLoginErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"upstream 401", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`},
		{"pre-charge 403", http.StatusForbidden, `{"error":{"message":"预扣费额度失败"}}`},
		{"claude login 403", http.StatusForbidden, `Please run /login · API Error: 403`},
		{"quota 429", http.StatusTooManyRequests, `{"error":{"type":"exceeded_current_quota_error"}}`},
		{"plain 429", http.StatusTooManyRequests, `{"error":{"message":"too many requests"}}`},
		{"upstream 503", http.StatusServiceUnavailable, `{"error":{"message":"no available channel"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var badCalls, goodCalls int
			bad := errorUpstream(tc.status, tc.body, &badCalls)
			defer bad.Close()
			good := errorUpstream(http.StatusOK, `{"type":"message","content":[]}`, &goodCalls)
			defer good.Close()

			h := failoverHandler(
				config.BackendAPI{Name: "bad", URL: bad.URL, APIKey: "k", Weight: 100, Enabled: true},
				config.BackendAPI{Name: "good", URL: good.URL, APIKey: "k", Weight: 1, Enabled: true},
			)

			w := forwardRequest(h, claudeCodeUA)

			if w.Code != http.StatusOK {
				t.Fatalf("client status = %d, want 200 (body=%s)", w.Code, w.Body.String())
			}
			if badCalls != 1 || goodCalls != 1 {
				t.Fatalf("calls: bad=%d good=%d, want 1 and 1", badCalls, goodCalls)
			}
			if h.lb.backends[0].effectiveWeight() != 0 {
				t.Fatal("the failing node must be ejected after the failover")
			}
		})
	}
}

// A request the caller got wrong is rejected identically everywhere, so it must
// come straight back rather than burning the pool.
func TestForward_DoesNotFailOverClientErrors(t *testing.T) {
	var badCalls, goodCalls int
	bad := errorUpstream(http.StatusBadRequest, `{"error":{"message":"max_tokens required"}}`, &badCalls)
	defer bad.Close()
	good := errorUpstream(http.StatusOK, `{"type":"message","content":[]}`, &goodCalls)
	defer good.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "bad", URL: bad.URL, APIKey: "k", Weight: 100, Enabled: true},
		config.BackendAPI{Name: "good", URL: good.URL, APIKey: "k", Weight: 1, Enabled: true},
	)

	w := forwardRequest(h, claudeCodeUA)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("client status = %d, want 400", w.Code)
	}
	if badCalls != 1 || goodCalls != 0 {
		t.Fatalf("calls: bad=%d good=%d, want 1 and 0", badCalls, goodCalls)
	}
}

// Once every node has been tried, Claude Code must be told the pool is
// overloaded — a 401/403 would make it stop and ask the user to run /login.
func TestForward_ExhaustedPoolReportsOverloadedToClaudeCode(t *testing.T) {
	var calls int
	bad := errorUpstream(http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, &calls)
	defer bad.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "only", URL: bad.URL, APIKey: "k", Weight: 10, Enabled: true},
	)

	w := forwardRequest(h, claudeCodeUA)

	if w.Code != statusOverloaded {
		t.Fatalf("client status = %d, want %d (body=%s)", w.Code, statusOverloaded, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no alternate backend)", calls)
	}
}

// Other clients keep the real status: only Claude Code mis-renders it.
func TestForward_ExhaustedPoolKeepsStatusForOtherClients(t *testing.T) {
	var calls int
	bad := errorUpstream(http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, &calls)
	defer bad.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "only", URL: bad.URL, APIKey: "k", Weight: 10, Enabled: true},
	)

	w := forwardRequest(h, "OpenAI/Python 1.0")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("client status = %d, want 401", w.Code)
	}
}

// The classification peek must not truncate what the client receives.
func TestForward_RelaysFullErrorBody(t *testing.T) {
	long := strings.Repeat("y", maxPeekBytes*2)
	var calls int
	bad := errorUpstream(http.StatusTooManyRequests, long, &calls)
	defer bad.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "only", URL: bad.URL, APIKey: "k", Weight: 10, Enabled: true},
	)

	w := forwardRequest(h, "OpenAI/Python 1.0")

	if w.Body.Len() != len(long) {
		t.Fatalf("client got %d bytes, want %d", w.Body.Len(), len(long))
	}
}

// A node that dies at the socket level is no different from one returning 5xx.
func TestForward_FailsOverTransportError(t *testing.T) {
	var goodCalls int
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing is listening now

	good := errorUpstream(http.StatusOK, `{"type":"message","content":[]}`, &goodCalls)
	defer good.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "dead", URL: deadURL, APIKey: "k", Weight: 100, Enabled: true},
		config.BackendAPI{Name: "good", URL: good.URL, APIKey: "k", Weight: 1, Enabled: true},
	)

	w := forwardRequest(h, claudeCodeUA)

	if w.Code != http.StatusOK {
		t.Fatalf("client status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if goodCalls != 1 {
		t.Fatalf("good backend calls = %d, want 1", goodCalls)
	}
}

// /v1/responses never went through forward(), so it had no failover at all.
func TestHandleResponses_FailsOverUpstream401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var badCalls, goodCalls int
	bad := errorUpstream(http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, &badCalls)
	defer bad.Close()
	good := errorUpstream(http.StatusOK,
		`{"id":"msg_1","model":"claude-opus-4-8","stop_reason":"end_turn",
		  "content":[{"type":"text","text":"hi"}],
		  "usage":{"input_tokens":10,"output_tokens":5}}`, &goodCalls)
	defer good.Close()

	h := failoverHandler(
		config.BackendAPI{Name: "bad", URL: bad.URL, APIKey: "k", Weight: 100, Enabled: true},
		config.BackendAPI{Name: "good", URL: good.URL, APIKey: "k", Weight: 1, Enabled: true},
	)

	body := responsesReq(false, "")
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	if w.Code != http.StatusOK {
		t.Fatalf("client status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if badCalls != 1 || goodCalls != 1 {
		t.Fatalf("calls: bad=%d good=%d, want 1 and 1", badCalls, goodCalls)
	}
}

// The failover chain is bounded so a wholly broken pool cannot stack latency.
func TestForward_StopsAtFailoverCap(t *testing.T) {
	var total int
	cfgs := make([]config.BackendAPI, 0, maxFailovers+3)
	servers := make([]*httptest.Server, 0, maxFailovers+3)
	for i := 0; i < maxFailovers+3; i++ {
		s := errorUpstream(http.StatusServiceUnavailable, `{"error":"down"}`, &total)
		servers = append(servers, s)
		cfgs = append(cfgs, config.BackendAPI{
			Name: string(rune('a' + i)), URL: s.URL, APIKey: "k", Weight: 10, Enabled: true,
		})
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	h := failoverHandler(cfgs...)
	w := forwardRequest(h, "OpenAI/Python 1.0")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("client status = %d, want 503", w.Code)
	}
	if total != maxFailovers+1 {
		t.Fatalf("upstream calls = %d, want %d", total, maxFailovers+1)
	}
}
