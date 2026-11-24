package proxy

import (
	"encoding/json"
	"testing"
)

func TestRewriteThinkingEnabledToAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":8000},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "enabled" {
		t.Errorf("originalType = %q; want enabled", origType)
	}
	if action != thinkingActionAdaptive {
		t.Errorf("action = %q; want %s", action, thinkingActionAdaptive)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	thinking := parsed["thinking"].(map[string]interface{})
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v; want adaptive", thinking["type"])
	}
	if _, exists := thinking["budget_tokens"]; exists {
		t.Errorf("budget_tokens still present after rewrite")
	}
}

func TestRewriteThinkingExtendedThinkingToAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","thinking":{"type":"extended_thinking"},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "extended_thinking" {
		t.Errorf("originalType = %q; want extended_thinking", origType)
	}
	if action != thinkingActionAdaptive {
		t.Errorf("action = %q; want %s", action, thinkingActionAdaptive)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	thinking := parsed["thinking"].(map[string]interface{})
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v; want adaptive", thinking["type"])
	}
}

func TestRewriteThinkingDisabledDeletes(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"disabled"},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "disabled" {
		t.Errorf("originalType = %q; want disabled", origType)
	}
	if action != thinkingActionDeleted {
		t.Errorf("action = %q; want %s", action, thinkingActionDeleted)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, exists := parsed["thinking"]; exists {
		t.Errorf("thinking field still present after delete")
	}
}

func TestRewriteThinkingEmptyTypeDeletes(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":""},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "" {
		t.Errorf("originalType = %q; want empty string", origType)
	}
	if action != thinkingActionDeleted {
		t.Errorf("action = %q; want %s", action, thinkingActionDeleted)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, exists := parsed["thinking"]; exists {
		t.Errorf("thinking field still present after delete")
	}
}

func TestRewriteThinkingAdaptiveUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "adaptive" {
		t.Errorf("originalType = %q; want adaptive", origType)
	}
	if action != "" {
		t.Errorf("action = %q; want empty (unchanged)", action)
	}

	if string(got) != string(body) {
		t.Errorf("body was modified when it should have been unchanged")
	}
}

func TestRewriteThinkingBetweenToolsUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "between_tools" {
		t.Errorf("originalType = %q; want between_tools", origType)
	}
	if action != "" {
		t.Errorf("action = %q; want empty (unchanged)", action)
	}

	if string(got) != string(body) {
		t.Errorf("body was modified when it should have been unchanged")
	}
}

func TestRewriteThinkingUnknownTypeDeletes(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"foobar"},"messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "foobar" {
		t.Errorf("originalType = %q; want foobar", origType)
	}
	if action != thinkingActionDeleted {
		t.Errorf("action = %q; want %s", action, thinkingActionDeleted)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if _, exists := parsed["thinking"]; exists {
		t.Errorf("thinking field still present after delete")
	}
}

func TestRewriteThinkingNoThinkingFieldUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "" {
		t.Errorf("originalType = %q; want empty", origType)
	}
	if action != "" {
		t.Errorf("action = %q; want empty (unchanged)", action)
	}

	if string(got) != string(body) {
		t.Errorf("body was modified when it should have been unchanged")
	}
}

func TestRewriteThinkingMalformedJSONUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled"`)
	got, origType, action := RewriteThinking(body)

	if origType != "" {
		t.Errorf("originalType = %q; want empty", origType)
	}
	if action != "" {
		t.Errorf("action = %q; want empty (unchanged)", action)
	}

	if string(got) != string(body) {
		t.Errorf("malformed body was modified")
	}
}

func TestRewriteThinkingNonObjectUnchanged(t *testing.T) {
	// thinking is a string instead of an object
	body := []byte(`{"model":"claude-opus-5-5","thinking":"enabled","messages":[]}`)
	got, origType, action := RewriteThinking(body)

	if origType != "" {
		t.Errorf("originalType = %q; want empty", origType)
	}
	if action != "" {
		t.Errorf("action = %q; want empty (unchanged)", action)
	}

	if string(got) != string(body) {
		t.Errorf("body with non-object thinking was modified")
	}
}

func TestRewriteThinkingPreservesKeyOrder(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled"},"max_tokens":1024,"messages":[]}`)
	got, _, _ := RewriteThinking(body)

	// Ensure the keys "model", "thinking", "max_tokens", "messages" appear in order
	gotStr := string(got)
	modelPos := indexOf(gotStr, `"model"`)
	thinkingPos := indexOf(gotStr, `"thinking"`)
	maxTokensPos := indexOf(gotStr, `"max_tokens"`)
	messagesPos := indexOf(gotStr, `"messages"`)

	if !(modelPos < thinkingPos && thinkingPos < maxTokensPos && maxTokensPos < messagesPos) {
		t.Errorf("key order not preserved: got %s", gotStr)
	}
}

func TestRewriteThinkingPreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"disabled"},"max_tokens":2048,"temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`)
	got, _, _ := RewriteThinking(body)

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if parsed["model"] != "claude-opus-5-5" {
		t.Errorf("model field changed")
	}
	if parsed["max_tokens"].(float64) != 2048 {
		t.Errorf("max_tokens field changed")
	}
	if parsed["temperature"].(float64) != 0.7 {
		t.Errorf("temperature field changed")
	}
	messages := parsed["messages"].([]interface{})
	if len(messages) != 1 {
		t.Errorf("messages array changed")
	}
}

func TestThinkingRewriteAppliesEmpty(t *testing.T) {
	if !thinkingRewriteApplies([]string{}, "claude-opus-5-5") {
		t.Errorf("empty list should match every model")
	}
}

func TestThinkingRewriteAppliesStar(t *testing.T) {
	if !thinkingRewriteApplies([]string{"*"}, "claude-opus-5-5") {
		t.Errorf("'*' should match every model")
	}
}

func TestThinkingRewriteAppliesSubstring(t *testing.T) {
	if !thinkingRewriteApplies([]string{"opus-5-5"}, "claude-opus-5-5") {
		t.Errorf("pattern substring should match")
	}
}

func TestThinkingRewriteAppliesCaseInsensitive(t *testing.T) {
	if !thinkingRewriteApplies([]string{"OPUS"}, "claude-opus-5-5") {
		t.Errorf("pattern match should be case-insensitive")
	}
}

func TestThinkingRewriteAppliesDoesNotMatch(t *testing.T) {
	if thinkingRewriteApplies([]string{"sonnet"}, "claude-opus-5-5") {
		t.Errorf("non-matching pattern should not match")
	}
}

// indexOf returns the index of substr in s, or -1 if not found
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
