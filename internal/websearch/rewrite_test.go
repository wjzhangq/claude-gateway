package websearch

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func rawTools(t *testing.T, tools ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(tools))
	for i, s := range tools {
		out[i] = json.RawMessage(s)
	}
	return out
}

func TestDetectWebSearchTool(t *testing.T) {
	tests := []struct {
		name        string
		tools       []json.RawMessage
		wantFound   bool
		wantMaxUses int
	}{
		{
			name:        "versioned server tool with max_uses",
			tools:       rawTools(t, `{"type":"web_search_20250305","name":"web_search","max_uses":3}`),
			wantFound:   true,
			wantMaxUses: 3,
		},
		{
			name:        "server tool without max_uses",
			tools:       rawTools(t, `{"type":"web_search_20990101","name":"web_search"}`),
			wantFound:   true,
			wantMaxUses: -1,
		},
		{
			name:        "only a custom tool",
			tools:       rawTools(t, `{"name":"Edit","input_schema":{"type":"object"}}`),
			wantFound:   false,
			wantMaxUses: -1,
		},
		{
			name:        "mixed, server tool present",
			tools:       rawTools(t, `{"name":"Bash","input_schema":{}}`, `{"type":"web_search_20250305","max_uses":5}`),
			wantFound:   true,
			wantMaxUses: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, maxUses, _, _ := DetectWebSearchTool(tc.tools)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if maxUses != tc.wantMaxUses {
				t.Fatalf("maxUses = %d, want %d", maxUses, tc.wantMaxUses)
			}
		})
	}
}

func TestDetectWebSearchToolDomains(t *testing.T) {
	tools := rawTools(t, `{"type":"web_search_20250305","allowed_domains":["a.com"],"blocked_domains":["b.com","c.com"]}`)
	found, _, allowed, blocked := DetectWebSearchTool(tools)
	if !found {
		t.Fatal("expected found")
	}
	if len(allowed) != 1 || allowed[0] != "a.com" {
		t.Fatalf("allowed = %v", allowed)
	}
	if len(blocked) != 2 {
		t.Fatalf("blocked = %v", blocked)
	}
}

func TestDetectInBody(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[],"tools":[{"type":"web_search_20250305"}]}`)
	if !DetectInBody(body) {
		t.Fatal("expected detection in body")
	}
	if DetectInBody([]byte(`{"model":"claude","messages":[]}`)) {
		t.Fatal("did not expect detection")
	}
	if DetectInBody([]byte(`not json`)) {
		t.Fatal("malformed body should not detect")
	}
}

func TestRewriteTools(t *testing.T) {
	req := &MessagesRequest{Tools: rawTools(t,
		`{"name":"Bash","input_schema":{}}`,
		`{"type":"web_search_20250305","max_uses":3}`,
	)}
	req.RewriteTools()
	if len(req.Tools) != 2 {
		t.Fatalf("want 2 tools after rewrite, got %d", len(req.Tools))
	}
	// server tool must be gone; client web_search tool must be present.
	for _, raw := range req.Tools {
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		if typ, _ := m["type"].(string); strings.HasPrefix(typ, ServerToolPrefix) {
			t.Fatal("server tool was not removed")
		}
	}
	last := req.Tools[len(req.Tools)-1]
	var m map[string]interface{}
	_ = json.Unmarshal(last, &m)
	if m["name"] != ClientToolName {
		t.Fatalf("injected tool name = %v, want %s", m["name"], ClientToolName)
	}
}

func TestNormalizeHistory(t *testing.T) {
	// An assistant turn carrying gateway server blocks must split into
	// assistant[tool_use] + user[tool_result], with encrypted_content decoded.
	snippet := "the answer is 42"
	enc := base64.StdEncoding.EncodeToString([]byte(snippet))
	body := `{
      "model":"claude","messages":[
        {"role":"user","content":"hi"},
        {"role":"assistant","content":[
          {"type":"text","text":"let me search"},
          {"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}},
          {"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
            {"type":"web_search_result","title":"T","url":"https://x.com","encrypted_content":"` + enc + `","page_age":"2026-01-01"}
          ]},
          {"type":"text","text":"done"}
        ]}
      ]}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	req.NormalizeHistory()

	// Expect: user(hi), assistant(text tool_use text), user(tool_result)
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(req.Messages))
	}
	asst := req.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("msg[1] role = %s", asst.Role)
	}
	var sawToolUse bool
	for _, b := range asst.Blocks {
		if b.Type == "server_tool_use" || b.Type == "web_search_tool_result" {
			t.Fatal("server blocks must be gone from assistant turn")
		}
		if b.Type == "tool_use" && b.Name == ClientToolName {
			sawToolUse = true
		}
	}
	if !sawToolUse {
		t.Fatal("expected a web_search tool_use block")
	}
	last := req.Messages[2]
	if last.Role != "user" || len(last.Blocks) != 1 || last.Blocks[0].Type != "tool_result" {
		t.Fatalf("want trailing user tool_result, got %+v", last)
	}
	var decoded string
	if err := json.Unmarshal(last.Blocks[0].Content, &decoded); err != nil {
		t.Fatalf("tool_result content not a string: %v", err)
	}
	if !strings.Contains(decoded, snippet) {
		t.Fatalf("decoded tool_result %q missing snippet %q", decoded, snippet)
	}
}
