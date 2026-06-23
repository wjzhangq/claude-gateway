package awsproxy

import (
	"encoding/json"
	"testing"

	"github.com/wjzhangq/claude-gateway/config"
)

// capsAdaptive / capsLegacy are the two capability rules used across tests.
var (
	capsAdaptive = config.ModelCapability{Thinking: "adaptive", OutputConfig: true}
	capsLegacy   = config.ModelCapability{Thinking: "legacy", OutputConfig: false}
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

	out := prepareAnthropicBody(body, capsAdaptive, config.AWSConfig{}.BodyFieldAllowlist())
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

	out := prepareAnthropicBody(body, capsLegacy, config.AWSConfig{}.BodyFieldAllowlist())
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
		body, _ := json.Marshal(map[string]any{
			"thinking":   map[string]any{"type": "enabled", "budget_tokens": tc.budget},
			"max_tokens": 32000,
			"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		})
		out := prepareAnthropicBody(body, capsAdaptive, config.AWSConfig{}.BodyFieldAllowlist())
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

// TestPrepareAnthropicBody_StripsUnknownFields reproduces the production
// "diagnostics: Extra inputs are not permitted" ValidationException and verifies
// the whitelist strips all non-allowlisted top-level fields.
func TestPrepareAnthropicBody_StripsUnknownFields(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"stream": true,
		"diagnostics": {"previous_message_id": null},
		"metadata": {"user_id": "abc"},
		"context_management": {"foo": "bar"},
		"max_tokens": 4000,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	out := prepareAnthropicBody(body, capsLegacy, config.AWSConfig{}.BodyFieldAllowlist())
	m := decode(t, out)

	for _, banned := range []string{"diagnostics", "stream", "metadata", "context_management", "model"} {
		if _, ok := m[banned]; ok {
			t.Errorf("field %q must be stripped, but it survived", banned)
		}
	}

	// Allowlisted fields must survive, and anthropic_version must be set.
	for _, kept := range []string{"messages", "max_tokens", "anthropic_version"} {
		if _, ok := m[kept]; !ok {
			t.Errorf("allowlisted field %q must be present", kept)
		}
	}
	var ver string
	_ = json.Unmarshal(m["anthropic_version"], &ver)
	if ver != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %q, want bedrock-2023-05-31", ver)
	}
}

func TestCapsFor(t *testing.T) {
	cfg := &config.AWSConfig{
		ModelCapabilities: []config.ModelCapability{
			{Match: "opus-4", Thinking: "adaptive", OutputConfig: true},
			{Match: "sonnet-4", Thinking: "legacy", OutputConfig: false},
			{Match: "haiku-4", Thinking: "legacy", OutputConfig: false},
		},
	}

	cases := []struct {
		name         string
		bedrock      string
		request      string
		wantThinking string
	}{
		{"opus-4-8 direct", "global.anthropic.claude-opus-4-8", "global.anthropic.claude-opus-4-8", "adaptive"},
		{"sonnet-4-6 direct", "global.anthropic.claude-sonnet-4-6", "global.anthropic.claude-sonnet-4-6", "legacy"},
		// bedrock is an inference-profile ARN with no model family — must fall back to requestModel.
		{"ARN + opus request", "arn:aws:bedrock:us-west-2:123:application-inference-profile/abc", "global.anthropic.claude-opus-4-8", "adaptive"},
		// no rule matches -> safe legacy default.
		{"unknown model", "some-future-model", "some-future-model", "legacy"},
	}
	for _, tc := range cases {
		got := cfg.CapsFor(tc.bedrock, tc.request)
		if got.Thinking != tc.wantThinking {
			t.Errorf("%s: CapsFor.Thinking = %q, want %q", tc.name, got.Thinking, tc.wantThinking)
		}
	}
}

func TestBodyFieldAllowlist_DefaultWhenEmpty(t *testing.T) {
	cfg := config.AWSConfig{} // no AllowedBodyFields configured
	allow := cfg.BodyFieldAllowlist()
	if len(allow) == 0 {
		t.Fatal("BodyFieldAllowlist returned empty for unconfigured AWSConfig")
	}
	set := map[string]bool{}
	for _, f := range allow {
		set[f] = true
	}
	// metadata must NOT be in the default allowlist (per decision).
	if set["metadata"] {
		t.Error("default allowlist must not contain metadata")
	}
	// core fields must be present.
	for _, must := range []string{"messages", "max_tokens", "system", "anthropic_version"} {
		if !set[must] {
			t.Errorf("default allowlist missing required field %q", must)
		}
	}
}

func TestBodyFieldAllowlist_UsesConfiguredWhenSet(t *testing.T) {
	cfg := config.AWSConfig{AllowedBodyFields: []string{"messages", "max_tokens"}}
	allow := cfg.BodyFieldAllowlist()
	if len(allow) != 2 {
		t.Fatalf("expected configured allowlist of 2, got %d", len(allow))
	}
}

// TestConvertOAIThenPrepare verifies the OpenAI path now flows through the same
// prepareAnthropicBody pipeline: anthropic_version must be correctly set (not the
// empty string the struct serializes by default) and the converted fields survive.
func TestConvertOAIThenPrepare(t *testing.T) {
	ar := convertOAIToAnthropic(openAIChatRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4000,
		Messages:  []openAIMsg{{Role: "user", Content: "hi"}},
	})
	raw, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("marshal converted request: %v", err)
	}

	out := prepareAnthropicBody(raw, capsLegacy, config.AWSConfig{}.BodyFieldAllowlist())
	m := decode(t, out)

	var ver string
	_ = json.Unmarshal(m["anthropic_version"], &ver)
	if ver != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %q, want bedrock-2023-05-31 (must be injected by prepareAnthropicBody)", ver)
	}
	for _, must := range []string{"messages", "max_tokens"} {
		if _, ok := m[must]; !ok {
			t.Errorf("converted field %q must survive prepareAnthropicBody", must)
		}
	}
}
