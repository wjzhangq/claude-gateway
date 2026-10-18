package proxy

import (
	"bytes"
	"compress/gzip"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
)

const okMessageBody = `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`

// failoverTestHandler builds a Handler whose backends point at urls, named
// b0, b1, ... in order. With equal weights the first Pick returns b0.
func failoverTestHandler(urls ...string) *Handler {
	lb := &LoadBalancer{
		nowFn: time.Now,
		rng:   rand.New(rand.NewSource(1)),
	}
	for i, u := range urls {
		lb.backends = append(lb.backends, lb.newBackend(config.BackendAPI{
			Name: "b" + string(rune('0'+i)), URL: u, APIKey: "k", Weight: 1, Enabled: true,
		}))
	}
	lb.updateCapacities()
	return NewHandler(lb, nil, nil, &config.Config{}, nil)
}

// statusServer replies with a fixed status/body and counts calls.
func statusServer(status int, body string, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func gzipServer(status int, body string, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write([]byte(body))
		zw.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(status)
		w.Write(buf.Bytes())
	}))
}

func doMessages(h *Handler, ua string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	body := `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if ua != "" {
		c.Request.Header.Set("User-Agent", ua)
	}
	h.Messages(c)
	return w
}

func backendByName(h *Handler, name string) *Backend {
	for _, b := range h.lb.backends {
		if b.Name == name {
			return b
		}
	}
	return nil
}

func TestFailover_UpstreamErrorsSwitchToNextBackend(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		gzip   bool
		quota  bool // backend must be isolated as quota-exhausted, not just forbidden
	}{
		{"login 403", http.StatusForbidden, `{"error":{"message":"预扣费额度失败, Please run /login"}}`, false, true},
		{"gzipped login 403", http.StatusForbidden, `{"error":{"message":"API Error: 403 · Please run /login"}}`, true, true},
		{"generic 403", http.StatusForbidden, `{"error":{"type":"permission_error","message":"forbidden"}}`, false, false},
		{"401", http.StatusUnauthorized, `{"error":{"message":"invalid token"}}`, false, false},
		{"quota 429", http.StatusTooManyRequests, `{"error":{"type":"exceeded_current_quota_error","message":"insufficient balance"}}`, false, true},
		{"rate limit 429", http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`, false, false},
		{"503", http.StatusServiceUnavailable, `{"error":{"message":"No available channel"}}`, false, false},
		{"529", 529, `{"type":"error","error":{"type":"overloaded_error"}}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var badCalls, goodCalls int
			var bad *httptest.Server
			if tc.gzip {
				bad = gzipServer(tc.status, tc.body, &badCalls)
			} else {
				bad = statusServer(tc.status, tc.body, &badCalls)
			}
			defer bad.Close()
			good := statusServer(http.StatusOK, okMessageBody, &goodCalls)
			defer good.Close()

			h := failoverTestHandler(bad.URL, good.URL)
			w := doMessages(h, "claude-cli/2.1.0 (external, cli)")

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
			}
			if badCalls != 1 || goodCalls != 1 {
				t.Fatalf("calls bad=%d good=%d, want 1/1", badCalls, goodCalls)
			}
			if isRoutable(backendByName(h, "b0").State()) {
				t.Fatalf("failed backend still routable (state=%s)", stateName(backendByName(h, "b0").State()))
			}
			if got := backendByName(h, "b0").quotaExhausted.Load(); got != tc.quota {
				t.Fatalf("quotaExhausted = %v, want %v", got, tc.quota)
			}
		})
	}
}

func TestFailover_TransportErrorSwitchesToNextBackend(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	var goodCalls int
	good := statusServer(http.StatusOK, okMessageBody, &goodCalls)
	defer good.Close()

	h := failoverTestHandler(deadURL, good.URL)
	w := doMessages(h, "")
	if w.Code != http.StatusOK || goodCalls != 1 {
		t.Fatalf("status=%d goodCalls=%d, want 200/1; body=%s", w.Code, goodCalls, w.Body.String())
	}
}

func TestFailover_RequestErrorsAreNotRetried(t *testing.T) {
	var badCalls, goodCalls int
	bad := statusServer(http.StatusBadRequest, `{"error":{"message":"prompt is too long"}}`, &badCalls)
	defer bad.Close()
	good := statusServer(http.StatusOK, okMessageBody, &goodCalls)
	defer good.Close()

	h := failoverTestHandler(bad.URL, good.URL)
	w := doMessages(h, "claude-cli/2.1.0")
	if w.Code != http.StatusBadRequest || badCalls != 1 || goodCalls != 0 {
		t.Fatalf("status=%d bad=%d good=%d, want 400/1/0", w.Code, badCalls, goodCalls)
	}
}

func TestFailover_BoundedAndClaudeSeesRetryable529(t *testing.T) {
	var calls [4]int
	var urls []string
	for i := range calls {
		s := statusServer(http.StatusForbidden, `{"error":{"message":"forbidden"}}`, &calls[i])
		defer s.Close()
		urls = append(urls, s.URL)
	}

	h := failoverTestHandler(urls...)
	w := doMessages(h, "claude-cli/2.1.0 (external, cli)")
	if w.Code != statusOverloaded {
		t.Fatalf("claude status = %d, want 529; body=%s", w.Code, w.Body.String())
	}
	total := 0
	for _, n := range calls {
		total += n
	}
	if total != maxFailovers+1 {
		t.Fatalf("attempts = %d, want %d", total, maxFailovers+1)
	}

	h2 := failoverTestHandler(urls...)
	w2 := doMessages(h2, "OpenAI/JS 4.0")
	if w2.Code != http.StatusForbidden {
		t.Fatalf("non-claude status = %d, want 403", w2.Code)
	}
}

func TestFailover_WebSearchFirstRound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var badCalls, goodCalls int
	bad := statusServer(http.StatusForbidden, `{"error":{"message":"Please run /login"}}`, &badCalls)
	defer bad.Close()
	good := mockUpstream(t, &goodCalls)
	defer good.Close()

	h := failoverTestHandler(bad.URL, good.URL)
	h.UpdateConfig(&config.Config{WebSearch: config.WebSearchConfig{
		Enabled: true, Provider: "searxng", SearchURL: searx.URL,
		MaxResults: 5, SnippetMaxChars: 200, Timeout: 2 * time.Second, DefaultMaxUses: 5,
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(wsRequestBody(false)))
	c.Request.Header.Set("Content-Type", "application/json")
	h.handleWithWebSearch(c, h.lb.Pick(), "/v1/messages", []byte(wsRequestBody(false)), "claude-opus-4-8", nil, "", time.Now(), false, false)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if badCalls != 1 || goodCalls != 2 {
		t.Fatalf("calls bad=%d good=%d, want 1/2", badCalls, goodCalls)
	}
}
