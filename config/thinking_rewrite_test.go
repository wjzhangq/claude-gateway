package config

import (
	"testing"
)

func TestThinkingRewriteConfig_ParsesYAML(t *testing.T) {
	body := minimalHead + `
thinking_rewrite:
  enabled: true
  apply_to_models:
    - "opus-5-5"
    - "sonnet-5-5"
  log_rewrites: true
`
	p := writeTempConfig(t, body)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.ThinkingRewrite.Enabled {
		t.Errorf("ThinkingRewrite.Enabled = false; want true")
	}

	want := []string{"opus-5-5", "sonnet-5-5"}
	if len(cfg.ThinkingRewrite.Models) != len(want) {
		t.Fatalf("ThinkingRewrite.Models = %v; want %v", cfg.ThinkingRewrite.Models, want)
	}
	for i, m := range want {
		if cfg.ThinkingRewrite.Models[i] != m {
			t.Errorf("ThinkingRewrite.Models[%d] = %q; want %q", i, cfg.ThinkingRewrite.Models[i], m)
		}
	}

	if !cfg.ThinkingRewrite.LogRewrites {
		t.Errorf("ThinkingRewrite.LogRewrites = false; want true")
	}
}

func TestThinkingRewriteConfig_DefaultsToDisabled(t *testing.T) {
	// No thinking_rewrite block → should default to disabled
	p := writeTempConfig(t, minimalHead)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ThinkingRewrite.Enabled {
		t.Errorf("ThinkingRewrite.Enabled = true; want false (default)")
	}

	if len(cfg.ThinkingRewrite.Models) != 0 {
		t.Errorf("ThinkingRewrite.Models = %v; want empty (default)", cfg.ThinkingRewrite.Models)
	}

	if cfg.ThinkingRewrite.LogRewrites {
		t.Errorf("ThinkingRewrite.LogRewrites = true; want false (default)")
	}
}
