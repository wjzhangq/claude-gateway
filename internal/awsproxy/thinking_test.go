package awsproxy

import (
	"encoding/json"
	"testing"
)

// decode unmarshals the prepared body for assertions.
func decode(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal prepared body: %v", err)
	}
	return m
}

func TestPrepareAnthropicBody_AdaptiveThinkingForOpus4(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-8",
		"stream": true,
		"max_tokens": 32000,
		"thinking": {"type": "enabled", "budget_tokens": 10000},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	out := prepareAnthropicBody(body, "global.anthropic.claude-opus-4-8", "global.anthropic.claude-opus-4-8")
	m := decode(t, out)

	// thinking must be converted to adaptive, with budget_tokens removed.
	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if err := json.Unmarshal(m["thinking"], &thinking); err != nil {
		t.Fatalf("thinking unmarshal: %v", err)
	}
	if thinking.Type != "adaptive" {
		t.Errorf("thinking.type = %q, want %q", thinking.Type, "adaptive")
	}
	if thinking.BudgetTokens != 0 {
		t.Errorf("budget_tokens = %d, want 0 (removed)", thinking.BudgetTokens)
	}

	// output_config.effort must be present (not stripped) for Opus 4.x.
	oc, ok := m["output_config"]
	if !ok {
		t.Fatal("output_config missing; must be preserved for Opus 4.x")
	}
	var ocParsed struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(oc, &ocParsed); err != nil {
		t.Fatalf("output_config unmarshal: %v", err)
	}
	if ocParsed.Effort != "medium" {
		t.Errorf("effort = %q, want %q (10000 budget -> medium)", ocParsed.Effort, "medium")
	}
}

func TestPrepareAnthropicBody_LegacyThinkingUnchanged(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-5-sonnet",
		"max_tokens": 4000,
		"thinking": {"type": "enabled", "budget_tokens": 8000},
		"output_config": {"effort": "high"},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	out := prepareAnthropicBody(body, "anthropic.claude-3-5-sonnet-20241022-v2:0", "anthropic.claude-3-5-sonnet-20241022-v2:0")
	m := decode(t, out)

	// Legacy models: output_config must be stripped.
	if _, ok := m["output_config"]; ok {
		t.Error("output_config must be stripped for legacy models")
	}

	// thinking.type stays enabled.
	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if err := json.Unmarshal(m["thinking"], &thinking); err != nil {
		t.Fatalf("thinking unmarshal: %v", err)
	}
	if thinking.Type != "enabled" {
		t.Errorf("thinking.type = %q, want %q", thinking.Type, "enabled")
	}

	// max_tokens must be bumped above budget_tokens (8000 -> 9024).
	var maxTokens int
	_ = json.Unmarshal(m["max_tokens"], &maxTokens)
	if maxTokens <= thinking.BudgetTokens {
		t.Errorf("max_tokens = %d, want > budget_tokens %d", maxTokens, thinking.BudgetTokens)
	}
}

func TestPrepareAnthropicBody_EffortMapping(t *testing.T) {
	cases := []struct {
		budget int
		want   string
	}{
		{2000, "low"},
		{4096, "low"},
		{10000, "medium"},
		{16384, "medium"},
		{32000, "high"},
		{0, "high"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]interface{}{
			"thinking":    map[string]interface{}{"type": "enabled", "budget_tokens": tc.budget},
			"max_tokens":  32000,
			"messages":    []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		})
		// bedrockModel is an inference-profile ARN with no model family in it;
		// detection must fall back to requestModel ("...opus-4-8").
		out := prepareAnthropicBody(body,
			"arn:aws:bedrock:us-west-2:123:application-inference-profile/abc123",
			"global.anthropic.claude-opus-4-8")
		m := decode(t, out)
		var oc struct {
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(m["output_config"], &oc)
		if oc.Effort != tc.want {
			t.Errorf("budget %d: effort = %q, want %q", tc.budget, oc.Effort, tc.want)
		}
	}
}

func TestModelUsesAdaptiveThinking(t *testing.T) {
	adaptive := []string{
		"global.anthropic.claude-opus-4-8",
		"claude-opus-4-6-v1",
		"anthropic.claude-opus-4-7",
	}
	legacy := []string{
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"claude-3-7-sonnet",
		"claude-sonnet-4-5",
	}
	for _, s := range adaptive {
		if !modelUsesAdaptiveThinking(s) {
			t.Errorf("modelUsesAdaptiveThinking(%q) = false, want true", s)
		}
	}
	for _, s := range legacy {
		if modelUsesAdaptiveThinking(s) {
			t.Errorf("modelUsesAdaptiveThinking(%q) = true, want false", s)
		}
	}
}
