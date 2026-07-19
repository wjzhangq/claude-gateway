package proxy

import (
	"encoding/json"
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

// wsTestHandler builds a Handler whose single backend points at upstreamURL and
// whose SearXNG client points at searxURL.
func wsTestHandler(t *testing.T, upstreamURL, searxURL string) *Handler {
	t.Helper()
	lb := &LoadBalancer{
		nowFn: time.Now,
		rng:   rand.New(rand.NewSource(1)),
	}
	lb.backends = append(lb.backends, lb.newBackend(config.BackendAPI{
		Name: "b", URL: upstreamURL, APIKey: "k", Weight: 1, Enabled: true,
	}))
	lb.updateCapacities()

	wsCfg := config.WebSearchConfig{
		Enabled:         true,
		Provider:        "searxng",
		SearchURL:       searxURL,
		MaxResults:      5,
		SnippetMaxChars: 200,
		Timeout:         2 * time.Second,
		DefaultMaxUses:  5,
	}
	cfg := &config.Config{WebSearch: wsCfg}
	return NewHandler(lb, nil, nil, cfg, nil)
}

// mockSearx returns a SearXNG server yielding one fixed result.
func mockSearx(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Result","url":"https://example.com","content":"a snippet","score":0.9,"publishedDate":"2026-07-01"}]}`))
	}))
}

// mockUpstream returns a backend that, on its FIRST call, asks for a web_search,
// and on subsequent calls returns a final text answer. It tracks call count.
func mockUpstream(t *testing.T, calls *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The forwarded request must have the server tool rewritten to a client tool.
		if strings.Contains(string(body), "web_search_20250305") {
			t.Errorf("server tool leaked to upstream: %s", body)
		}
		*calls++
		w.Header().Set("Content-Type", "application/json")
		if *calls == 1 {
			w.Write([]byte(`{
				"id":"msg_up","type":"message","role":"assistant","model":"claude-x",
				"content":[
					{"type":"text","text":"Let me search."},
					{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"golang"}}
				],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":10,"output_tokens":5}
			}`))
			return
		}
		w.Write([]byte(`{
			"id":"msg_up2","type":"message","role":"assistant","model":"claude-x",
			"content":[{"type":"text","text":"The answer is Go."}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":20,"output_tokens":8}
		}`))
	}))
}

func wsRequestBody(stream bool) string {
	b := map[string]interface{}{
		"model":      "claude-opus-4-8",
		"max_tokens": 1024,
		"stream":     stream,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "search the web for golang"},
		},
		"tools": []map[string]interface{}{
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
		},
	}
	out, _ := json.Marshal(b)
	return string(out)
}

func TestWebSearchLoop_NonStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var calls int
	up := mockUpstream(t, &calls)
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(wsRequestBody(false)))
	c.Request.Header.Set("Content-Type", "application/json")

	backend := h.lb.Pick()
	h.handleWithWebSearch(c, backend, "/v1/messages", []byte(wsRequestBody(false)), "claude-opus-4-8", nil, "", time.Now(), false, false)

	if calls != 2 {
		t.Fatalf("expected 2 upstream calls (search + final), got %d", calls)
	}

	var resp websearch.MessagesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, w.Body.String())
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q", resp.StopReason)
	}
	// Content must contain server_tool_use + web_search_tool_result + final text.
	types := map[string]int{}
	for _, b := range resp.Content {
		types[b.Type]++
	}
	if types["server_tool_use"] != 1 {
		t.Fatalf("want 1 server_tool_use, got %d (%+v)", types["server_tool_use"], types)
	}
	if types["web_search_tool_result"] != 1 {
		t.Fatalf("want 1 web_search_tool_result, got %d", types["web_search_tool_result"])
	}
	if types["text"] < 1 {
		t.Fatalf("want text blocks, got %d", types["text"])
	}
	// Aggregated usage across both rounds.
	if resp.Usage.InputTokens != 30 || resp.Usage.OutputTokens != 13 {
		t.Fatalf("usage not aggregated: %+v", resp.Usage)
	}
	if resp.Usage.ServerToolUse == nil || resp.Usage.ServerToolUse.WebSearchRequests != 1 {
		t.Fatalf("web_search_requests: %+v", resp.Usage.ServerToolUse)
	}
}

func TestWebSearchLoop_Stream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var calls int
	up := mockUpstream(t, &calls)
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(wsRequestBody(true)))
	c.Request.Header.Set("Content-Type", "application/json")

	backend := h.lb.Pick()
	h.handleWithWebSearch(c, backend, "/v1/messages", []byte(wsRequestBody(true)), "claude-opus-4-8", nil, "", time.Now(), false, false)

	out := w.Body.String()
	// Must be a well-formed SSE stream bracketed by message_start and message_stop.
	if !strings.Contains(out, "event: message_start") {
		t.Fatalf("missing message_start:\n%s", out)
	}
	if !strings.Contains(out, "event: message_stop") {
		t.Fatalf("missing message_stop")
	}
	if !strings.Contains(out, "web_search_tool_result") {
		t.Fatalf("missing web_search_tool_result block in stream")
	}
	if !strings.Contains(out, "server_tool_use") {
		t.Fatalf("missing server_tool_use block in stream")
	}
	if !strings.Contains(out, "event: message_delta") {
		t.Fatalf("missing message_delta")
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestWebSearchLoop_MaxUses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()

	// Upstream keeps requesting web_search forever; the cap must stop it.
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"m","type":"message","role":"assistant","model":"c",
			"content":[{"type":"tool_use","id":"t","name":"web_search","input":{"query":"x"}}],
			"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)
	// Force a tiny cap.
	h.webSearchCfg.DefaultMaxUses = 2

	body := `{"model":"c","max_tokens":100,"stream":false,"messages":[{"role":"user","content":"go"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))

	backend := h.lb.Pick()
	h.handleWithWebSearch(c, backend, "/v1/messages", []byte(body), "c", nil, "", time.Now(), false, false)

	// Bounded by maxWebSearchRounds regardless of the model's persistence.
	if calls > maxWebSearchRounds+1 {
		t.Fatalf("loop not bounded: %d calls", calls)
	}
	var resp websearch.MessagesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A max_uses_exceeded error block should appear once the cap is hit.
	found := false
	for _, b := range resp.Content {
		if b.Type == "web_search_tool_result" && strings.Contains(string(b.Content), "max_uses_exceeded") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a max_uses_exceeded result block; content=%s", w.Body.String())
	}
}
