package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/responses"
)

// responsesReq builds a /v1/responses request body.
func responsesReq(stream bool, tools string) string {
	return `{
		"model":"claude-opus-4-8",
		"input":"search the web for golang",
		"stream":` + boolStr(stream) + `,
		"tools":[` + tools + `]
	}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// newResponsesCtx creates a gin context for a POST /v1/responses request.
func newResponsesCtx(body string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

func TestHandleResponses_WebSearch_JSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var calls int
	up := mockUpstream(t, &calls)
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)

	body := responsesReq(false, `{"type":"web_search"}`)
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	if calls != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", calls)
	}

	var resp responses.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, w.Body.String())
	}
	if resp.Object != "response" || resp.Status != "completed" {
		t.Fatalf("top-level wrong: %+v", resp)
	}
	if resp.Store {
		t.Fatalf("store must be false")
	}
	// Expect a web_search_call item and a message item.
	var sawSearch, sawMsg bool
	for _, it := range resp.Output {
		switch it.Type {
		case "web_search_call":
			sawSearch = true
			if it.Action == nil || it.Action.Query == "" {
				t.Fatalf("web_search_call missing query: %+v", it)
			}
		case "message":
			sawMsg = true
			if len(it.Content) == 0 || it.Content[0].Text == "" {
				t.Fatalf("message item empty: %+v", it)
			}
		}
	}
	if !sawSearch || !sawMsg {
		t.Fatalf("missing items: search=%v msg=%v (%+v)", sawSearch, sawMsg, resp.Output)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 30 || resp.Usage.OutputTokens != 13 {
		t.Fatalf("usage not aggregated: %+v", resp.Usage)
	}
}

func TestHandleResponses_WebSearch_Stream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var calls int
	up := mockUpstream(t, &calls)
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)

	body := responsesReq(true, `{"type":"web_search"}`)
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	out := w.Body.String()
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	for _, want := range []string{
		"event: response.created",
		"event: response.web_search_call.searching",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
}

// mockUpstreamPlain returns a single-round upstream that answers with plain text
// and never requests a search — the pure function-tool / no-web-search case.
func mockUpstreamPlain(t *testing.T, calls *int, seenBody *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*seenBody = string(b)
		*calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"m","type":"message","role":"assistant","model":"claude-x",
			"content":[{"type":"text","text":"hi there"}],
			"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4}
		}`))
	}))
}

func TestHandleResponses_FunctionToolOnly_SingleRound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	searx := mockSearx(t)
	defer searx.Close()
	var calls int
	var seen string
	up := mockUpstreamPlain(t, &calls, &seen)
	defer up.Close()

	h := wsTestHandler(t, up.URL, searx.URL)

	body := `{
		"model":"claude-opus-4-8",
		"input":"hello",
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]
	}`
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
	// The function tool must reach upstream as an Anthropic tool with input_schema.
	if !strings.Contains(seen, `"input_schema"`) || !strings.Contains(seen, `"shell"`) {
		t.Fatalf("function tool not forwarded as anthropic tool: %s", seen)
	}
	// No web_search client tool should have been injected.
	if strings.Contains(seen, `"name":"web_search"`) {
		t.Fatalf("unexpected web_search tool injected: %s", seen)
	}

	var resp responses.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, w.Body.String())
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" || resp.Output[0].Content[0].Text != "hi there" {
		t.Fatalf("output wrong: %+v", resp.Output)
	}
}

func TestHandleResponses_RejectsPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := wsTestHandler(t, "http://127.0.0.1:0", "http://127.0.0.1:0")

	body := `{"model":"m","input":"x","previous_response_id":"resp_1"}`
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "previous_response_id") {
		t.Fatalf("error should mention previous_response_id: %s", w.Body.String())
	}
}

func TestHandleResponses_WebSearchDisabled_StripsServerTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls int
	var seen string
	up := mockUpstreamPlain(t, &calls, &seen)
	defer up.Close()

	h := wsTestHandler(t, up.URL, "http://127.0.0.1:0")
	// Disable the web-search feature.
	h.webSearchCfg.Enabled = false
	h.searxng = nil

	body := responsesReq(false, `{"type":"web_search"}`)
	w, c := newResponsesCtx(body)
	h.HandleResponses(c, []byte(body))

	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
	if strings.Contains(seen, "web_search") {
		t.Fatalf("web_search tool must be stripped when disabled: %s", seen)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
