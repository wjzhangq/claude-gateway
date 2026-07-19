package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// --- inbound: Responses -> Anthropic ---

func TestToMessagesRequest_BareStringInput(t *testing.T) {
	req, err := DecodeRequest([]byte(`{"model":"claude-x","input":"hello there","stream":true}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	anth, hasWS, err := ToMessagesRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if hasWS {
		t.Fatalf("did not expect web_search")
	}
	if anth.Model != "claude-x" || !anth.Stream {
		t.Fatalf("model/stream not carried: %+v", anth)
	}
	if len(anth.Messages) != 1 || anth.Messages[0].Role != "user" {
		t.Fatalf("expected 1 user message, got %+v", anth.Messages)
	}
	if len(anth.Messages[0].Blocks) != 1 || anth.Messages[0].Blocks[0].Text != "hello there" {
		t.Fatalf("text block wrong: %+v", anth.Messages[0].Blocks)
	}
	if string(anth.Extra["max_tokens"]) != "4096" {
		t.Fatalf("default max_tokens missing: %s", anth.Extra["max_tokens"])
	}
}

func TestToMessagesRequest_InstructionsBecomeSystem(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{
		"model":"m",
		"instructions":"be terse",
		"input":[{"type":"message","role":"system","content":"also be kind"},
		         {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
	}`))
	anth, _, err := ToMessagesRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var sys string
	if err := json.Unmarshal(anth.Extra["system"], &sys); err != nil {
		t.Fatalf("system not a string: %s", anth.Extra["system"])
	}
	if !strings.Contains(sys, "be terse") || !strings.Contains(sys, "also be kind") {
		t.Fatalf("system missing parts: %q", sys)
	}
	// system-role message must not appear as a turn.
	for _, m := range anth.Messages {
		if m.Role == "system" {
			t.Fatalf("system role leaked into messages")
		}
	}
	if len(anth.Messages) != 1 || anth.Messages[0].Blocks[0].Text != "hi" {
		t.Fatalf("user turn wrong: %+v", anth.Messages)
	}
}

func TestToMessagesRequest_FunctionCallRoundTrip(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{
		"model":"m",
		"input":[
			{"type":"message","role":"user","content":"run ls"},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"file.txt"}
		],
		"tools":[{"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]
	}`))
	anth, hasWS, err := ToMessagesRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if hasWS {
		t.Fatalf("no web_search expected")
	}
	// Expect: user(text) , assistant(tool_use), user(tool_result)
	if len(anth.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(anth.Messages), anth.Messages)
	}
	tu := anth.Messages[1]
	if tu.Role != "assistant" || tu.Blocks[0].Type != "tool_use" || tu.Blocks[0].ID != "call_1" || tu.Blocks[0].Name != "shell" {
		t.Fatalf("tool_use wrong: %+v", tu)
	}
	if string(tu.Blocks[0].Input) != `{"cmd":"ls"}` {
		t.Fatalf("tool_use input wrong: %s", tu.Blocks[0].Input)
	}
	tr := anth.Messages[2]
	if tr.Role != "user" || tr.Blocks[0].Type != "tool_result" || tr.Blocks[0].ToolUseID != "call_1" {
		t.Fatalf("tool_result wrong: %+v", tr)
	}
	var content string
	_ = json.Unmarshal(tr.Blocks[0].Content, &content)
	if content != "file.txt" {
		t.Fatalf("tool_result content wrong: %q", content)
	}
	// function tool -> anthropic tool with input_schema
	if len(anth.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(anth.Tools))
	}
	if !strings.Contains(string(anth.Tools[0]), `"input_schema"`) || !strings.Contains(string(anth.Tools[0]), `"name":"shell"`) {
		t.Fatalf("function tool not converted: %s", anth.Tools[0])
	}
}

func TestToMessagesRequest_WebSearchToolInjected(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{"model":"m","input":"x","tools":[{"type":"web_search"}]}`))
	anth, hasWS, err := ToMessagesRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !hasWS {
		t.Fatalf("expected web_search detected")
	}
	found, _, _, _ := websearch.DetectWebSearchTool(anth.Tools)
	if !found {
		t.Fatalf("anthropic web_search server tool not injected: %v", anth.Tools)
	}
}

func TestToMessagesRequest_DropsWebSearchCallHistory(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{
		"model":"m",
		"input":[
			{"type":"message","role":"user","content":"q"},
			{"type":"web_search_call","id":"ws_1","status":"completed"},
			{"type":"reasoning","id":"r_1"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]}
		]
	}`))
	anth, _, err := ToMessagesRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// only the two message items survive
	if len(anth.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(anth.Messages), anth.Messages)
	}
	if anth.Messages[0].Role != "user" || anth.Messages[1].Role != "assistant" {
		t.Fatalf("roles wrong: %+v", anth.Messages)
	}
}

func TestToMessagesRequest_RejectsPreviousResponseID(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{"model":"m","input":"x","previous_response_id":"resp_abc"}`))
	_, _, err := ToMessagesRequest(req)
	if err == nil {
		t.Fatalf("expected error for previous_response_id")
	}
}

func TestToMessagesRequest_MaxOutputTokens(t *testing.T) {
	req, _ := DecodeRequest([]byte(`{"model":"m","input":"x","max_output_tokens":123,"temperature":0.5}`))
	anth, _, _ := ToMessagesRequest(req)
	if string(anth.Extra["max_tokens"]) != "123" {
		t.Fatalf("max_tokens: %s", anth.Extra["max_tokens"])
	}
	if string(anth.Extra["temperature"]) != "0.5" {
		t.Fatalf("temperature: %s", anth.Extra["temperature"])
	}
}

// --- outbound: Anthropic -> Responses ---

func TestToResponse_TextAndWebSearch(t *testing.T) {
	blocks := []websearch.ContentBlock{
		websearch.ServerToolUseBlock("srvtoolu_1", "golang release"),
		websearch.WebSearchResultBlock("srvtoolu_1", nil, nil),
		{Type: "text", Text: "Go 1.99 is out."},
	}
	usage := websearch.Usage{InputTokens: 10, OutputTokens: 5}
	resp := ToResponse("claude-x", blocks, usage, "end_turn")

	if resp.Object != "response" || resp.Status != "completed" || resp.Store {
		t.Fatalf("top-level wrong: %+v", resp)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("expected 2 output items (web_search_call + message), got %d: %+v", len(resp.Output), resp.Output)
	}
	if resp.Output[0].Type != "web_search_call" || resp.Output[0].Action == nil || resp.Output[0].Action.Query != "golang release" {
		t.Fatalf("web_search_call wrong: %+v", resp.Output[0])
	}
	msg := resp.Output[1]
	if msg.Type != "message" || len(msg.Content) != 1 || msg.Content[0].Text != "Go 1.99 is out." {
		t.Fatalf("message item wrong: %+v", msg)
	}
	if msg.Content[0].Annotations == nil {
		t.Fatalf("annotations should be non-nil empty slice")
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage total wrong: %+v", resp.Usage)
	}
}

func TestToResponse_FunctionCall(t *testing.T) {
	blocks := []websearch.ContentBlock{
		{Type: "tool_use", ID: "call_9", Name: "shell", Input: json.RawMessage(`{"cmd":"ls"}`)},
	}
	resp := ToResponse("m", blocks, websearch.Usage{}, "tool_use")
	if len(resp.Output) != 1 || resp.Output[0].Type != "function_call" {
		t.Fatalf("expected function_call item: %+v", resp.Output)
	}
	fc := resp.Output[0]
	if fc.CallID != "call_9" || fc.Name != "shell" || fc.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("function_call fields wrong: %+v", fc)
	}
}

func TestToResponse_MaxTokensIncomplete(t *testing.T) {
	resp := ToResponse("m", []websearch.ContentBlock{{Type: "text", Text: "partial"}}, websearch.Usage{}, "max_tokens")
	if resp.Status != "incomplete" || resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("incomplete mapping wrong: %+v", resp)
	}
}

// --- SSE synthesis ---

func TestSSEWriter_EventSequence(t *testing.T) {
	var sb strings.Builder
	w := NewSSEWriter(&sb, nil, "claude-x")
	blocks := []websearch.ContentBlock{
		websearch.ServerToolUseBlock("srvtoolu_1", "q"),
		websearch.WebSearchResultBlock("srvtoolu_1", nil, nil),
		{Type: "text", Text: "answer"},
	}
	w.WriteResponse(blocks, websearch.Usage{InputTokens: 1, OutputTokens: 2}, "end_turn")

	out := sb.String()
	// Extract the event: lines in order.
	var events []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",       // web_search_call
		"response.web_search_call.in_progress",
		"response.web_search_call.searching",
		"response.web_search_call.completed",
		"response.output_item.done",
		"response.output_item.added",       // message
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(want) {
		t.Fatalf("event count mismatch: got %d %v", len(events), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d: got %q want %q\nfull: %v", i, events[i], want[i], events)
		}
	}

	// sequence_number must be monotonic starting at 0.
	assertMonotonicSeq(t, out)
}

func assertMonotonicSeq(t *testing.T, sseText string) {
	t.Helper()
	prev := -1
	for _, line := range strings.Split(sseText, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload struct {
			Seq *int `json:"sequence_number"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			continue
		}
		if payload.Seq == nil {
			t.Fatalf("event missing sequence_number: %s", line)
		}
		if *payload.Seq != prev+1 {
			t.Fatalf("sequence_number not monotonic: got %d after %d", *payload.Seq, prev)
		}
		prev = *payload.Seq
	}
	if prev < 0 {
		t.Fatalf("no sequence numbers found")
	}
}
