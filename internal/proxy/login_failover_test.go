package proxy

import (
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

func TestIsLoginPrompt_PhrasesAndPages(t *testing.T) {
	htmlLogin := []byte(`<!doctype html><html><title>Login</title><form><input type="password"></form></html>`)
	message := []byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"please login to continue"}]}`)
	cases := []struct {
		name   string
		status int
		header http.Header
		body   []byte
		want   bool
	}{
		{"precharge", 403, jsonHeader(), []byte(`{"error":{"message":"预扣费额度失败, 用户剩余额度: 0"}}`), true},
		{"please run login", 403, jsonHeader(), []byte(`Please run /login · API Error: 403`), true},
		{"chinese login", 401, jsonHeader(), []byte(`{"error":{"message":"请登录后重试"}}`), true},
		{"session expired", 403, jsonHeader(), []byte(`{"error":{"message":"Session expired"}}`), true},
		{"invalid key", 401, jsonHeader(), []byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`), false},
		{"html login 200", 200, htmlHeader(), htmlLogin, true},
		{"redirect", 302, http.Header{"Location": []string{"https://node.example/login"}}, nil, true},
		{"model mentions login", 200, jsonHeader(), message, false},
		{"plain 500", 500, jsonHeader(), []byte(`{"type":"error","error":{"message":"overloaded"}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLoginPrompt(tc.status, tc.header, tc.body); got != tc.want {
				t.Fatalf("isLoginPrompt = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInspectLoginPrompt_PreservesMessageBody(t *testing.T) {
	body := `{"id":"msg_1","type":"message","content":[{"type":"text","text":"please login to the console and then continue"}]}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     jsonHeader(),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	if _, ok := inspectLoginPrompt(resp); ok {
		t.Fatal("a normal message that mentions login must not failover")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("body was not restored: %s", got)
	}
}

func TestDoRequestWithFailover_SwitchesOffLoginPrompt(t *testing.T) {
	var badHits, goodHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		if got := r.Header.Get("Accept-Encoding"); got != "" {
			t.Errorf("Accept-Encoding = %q, want stripped", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"请重新登录"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}]}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	c := failoverGinContext()
	c.Request.Header.Set("Accept-Encoding", "gzip")

	gotBE, resp, err := h.doRequestWithFailover(c, badBE, "/v1/messages", []byte(`{"model":"claude-opus-4-8"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the next node", resp.StatusCode)
	}
	if gotBE.Name != "good" {
		t.Fatalf("backend = %s, want good", gotBE.Name)
	}
	if badHits != 1 || goodHits != 1 {
		t.Fatalf("hits bad=%d good=%d, want 1 and 1", badHits, goodHits)
	}
	if !badBE.quotaExhausted.Load() {
		t.Fatal("login prompt must circuit-break the node")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "msg_ok") {
		t.Fatalf("body = %s", body)
	}
}

func TestDoRequestWithFailover_HTMLLoginPageSwitches(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>登录</title></head><body><form action="/login"><input type="password" name="password"></form></body></html>`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_ok","type":"message","content":[{"type":"text","text":"ok"}]}`)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotBE.Name != "good" {
		t.Fatalf("status=%d backend=%s, want 200 from good", resp.StatusCode, gotBE.Name)
	}
}

func TestDoRequestWithFailover_InvalidKeyStays(t *testing.T) {
	var goodHits int
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	h, badBE := failoverHandler(t, bad.URL, good.URL)
	gotBE, resp, err := h.doRequestWithFailover(failoverGinContext(), badBE, "/v1/messages", []byte(`{}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || gotBE.Name != "bad" {
		t.Fatalf("status=%d backend=%s, want 401 from bad", resp.StatusCode, gotBE.Name)
	}
	if goodHits != 0 {
		t.Fatalf("invalid key must not failover, good hits=%d", goodHits)
	}
	if badBE.quotaExhausted.Load() {
		t.Fatal("invalid key must not be marked quota-exhausted")
	}
}

func TestRunWebSearchLoop_FailoversLoginOnFirstRound(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"未登录"}}`)
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
	if len(res.Merged) != 1 || res.Merged[0].Text != "ok" {
		t.Fatalf("merged = %+v", res.Merged)
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
	h := NewHandler(lb, nil, nil, &config.Config{}, nil)
	return h, badBE
}

func failoverGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c
}

func jsonHeader() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func htmlHeader() http.Header {
	return http.Header{"Content-Type": []string{"text/html"}}
}
