package websearch

import (
	"encoding/json"
	"testing"
)

func TestDecodeEncodePreservesExtraFields(t *testing.T) {
	body := `{
		"model":"claude-opus-4-8",
		"max_tokens":1024,
		"system":"you are helpful",
		"thinking":{"type":"enabled","budget_tokens":2000},
		"tool_choice":{"type":"auto"},
		"metadata":{"user_id":"u1"},
		"stream":true,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305"}]
	}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "claude-opus-4-8" {
		t.Fatalf("model = %q", req.Model)
	}
	if !req.Stream {
		t.Fatal("stream should be true")
	}

	out, err := req.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	for _, k := range []string{"system", "thinking", "tool_choice", "metadata", "max_tokens", "messages", "tools", "model", "stream"} {
		if _, ok := top[k]; !ok {
			t.Errorf("field %q lost on round-trip", k)
		}
	}
}

func TestStreamToggleReEncodes(t *testing.T) {
	req, err := DecodeRequest([]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	req.Stream = false
	out, _ := req.Encode()
	var top struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(out, &top)
	if top.Stream {
		t.Fatal("stream should re-encode as false")
	}
}

func TestBareStringContentRoundTrip(t *testing.T) {
	req, err := DecodeRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, _ := req.Encode()
	var top struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	// unmodified bare-string content should re-emit as a string
	var s string
	if err := json.Unmarshal(top.Messages[0].Content, &s); err != nil {
		t.Fatalf("content should be a string: %s", top.Messages[0].Content)
	}
	if s != "hello" {
		t.Fatalf("content = %q", s)
	}
}

func TestPassthroughBlockPreserved(t *testing.T) {
	// A thinking block should round-trip verbatim via Raw.
	body := `{"model":"m","messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"deep thoughts","signature":"sig"},
		{"type":"text","text":"answer"}
	]}]}`
	req, err := DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, _ := req.Encode()
	if !json.Valid(out) {
		t.Fatal("invalid json output")
	}
	var top struct {
		Messages []struct {
			Content []map[string]interface{} `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(out, &top)
	block := top.Messages[0].Content[0]
	if block["type"] != "thinking" || block["thinking"] != "deep thoughts" || block["signature"] != "sig" {
		t.Fatalf("thinking block not preserved: %+v", block)
	}
}
