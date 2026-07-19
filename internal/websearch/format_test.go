package websearch

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFormatForModel(t *testing.T) {
	results := []Result{
		{Title: "Go", URL: "https://go.dev", Content: "The Go language", PublishedAt: "2026-01-01"},
		{Title: "Rust", URL: "https://rust-lang.org", Content: "Systems language"},
	}
	out := FormatForModel(results, nil)
	if !strings.Contains(out, "[1] Go — https://go.dev (2026-01-01)") {
		t.Fatalf("missing entry 1: %q", out)
	}
	if !strings.Contains(out, "[2] Rust — https://rust-lang.org") {
		t.Fatalf("missing entry 2: %q", out)
	}
	if !strings.Contains(out, "The Go language") {
		t.Fatalf("missing snippet: %q", out)
	}
}

func TestFormatForModelError(t *testing.T) {
	out := FormatForModel(nil, errors.New("boom"))
	if !strings.Contains(strings.ToLower(out), "unavailable") {
		t.Fatalf("error text should mention unavailable: %q", out)
	}
	if empty := FormatForModel(nil, nil); !strings.Contains(strings.ToLower(empty), "no results") {
		t.Fatalf("empty text: %q", empty)
	}
}

func TestServerToolUseBlock(t *testing.T) {
	b := ServerToolUseBlock("srvtoolu_x", "golang generics")
	if b.Type != "server_tool_use" || b.ID != "srvtoolu_x" || b.Name != ClientToolName {
		t.Fatalf("bad block: %+v", b)
	}
	var in WebSearchInput
	if err := json.Unmarshal(b.Input, &in); err != nil || in.Query != "golang generics" {
		t.Fatalf("input: %v / %q", err, in.Query)
	}
}

func TestWebSearchResultBlock(t *testing.T) {
	res := []Result{{Title: "T", URL: "https://x.com", Content: "hello", PublishedAt: "2026-01-01"}}
	b := WebSearchResultBlock("srvtoolu_x", res, nil)
	if b.Type != "web_search_tool_result" || b.ToolUseID != "srvtoolu_x" {
		t.Fatalf("bad block: %+v", b)
	}
	var items []webSearchResultItem
	if err := json.Unmarshal(b.Content, &items); err != nil {
		t.Fatalf("content decode: %v", err)
	}
	if len(items) != 1 || items[0].Type != "web_search_result" {
		t.Fatalf("items: %+v", items)
	}
	dec, err := base64.StdEncoding.DecodeString(items[0].EncryptedContent)
	if err != nil || string(dec) != "hello" {
		t.Fatalf("encrypted_content not base64(snippet): %v / %q", err, dec)
	}
}

func TestErrorBlock(t *testing.T) {
	b := ErrorBlock("srvtoolu_x", "max_uses_exceeded")
	var e webSearchResultError
	if err := json.Unmarshal(b.Content, &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Type != "web_search_tool_result_error" || e.ErrorCode != "max_uses_exceeded" {
		t.Fatalf("bad error content: %+v", e)
	}
}

func TestSplitResponse(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "text", Text: "hi"},
		{Type: "tool_use", Name: ClientToolName, ID: "1"},
		{Type: "tool_use", Name: "Bash", ID: "2"},
		{Type: "thinking"},
	}
	web, client, others := SplitResponse(blocks)
	if len(web) != 1 || web[0].ID != "1" {
		t.Fatalf("web calls: %+v", web)
	}
	if len(client) != 1 || client[0].Name != "Bash" {
		t.Fatalf("client calls: %+v", client)
	}
	if len(others) != 2 {
		t.Fatalf("others: %+v", others)
	}
}

func TestUsageAdd(t *testing.T) {
	var u Usage
	u.Add(Usage{InputTokens: 10, OutputTokens: 5})
	u.Add(Usage{InputTokens: 3, OutputTokens: 2, ServerToolUse: &ServerToolUse{WebSearchRequests: 1}})
	if u.InputTokens != 13 || u.OutputTokens != 7 {
		t.Fatalf("tokens: %+v", u)
	}
	if u.ServerToolUse == nil || u.ServerToolUse.WebSearchRequests != 1 {
		t.Fatalf("server tool use: %+v", u.ServerToolUse)
	}
}
