package config

import (
	"testing"
)

func TestVisionRewriteConfig_ParsesYAML(t *testing.T) {
	body := minimalHead + `
vision_rewrite:
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

	if !cfg.VisionRewrite.Enabled {
		t.Errorf("VisionRewrite.Enabled = false; want true")
	}

	want := []string{"opus-5-5", "sonnet-5-5"}
	if len(cfg.VisionRewrite.Models) != len(want) {
		t.Fatalf("VisionRewrite.Models = %v; want %v", cfg.VisionRewrite.Models, want)
	}
	for i, m := range want {
		if cfg.VisionRewrite.Models[i] != m {
			t.Errorf("VisionRewrite.Models[%d] = %q; want %q", i, cfg.VisionRewrite.Models[i], m)
		}
	}

	if !cfg.VisionRewrite.LogRewrites {
		t.Errorf("VisionRewrite.LogRewrites = false; want true")
	}
}

func TestVisionRewriteConfig_DefaultsToDisabled(t *testing.T) {
	// No vision_rewrite block → should default to disabled
	p := writeTempConfig(t, minimalHead)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.VisionRewrite.Enabled {
		t.Errorf("VisionRewrite.Enabled = true; want false (default)")
	}

	if len(cfg.VisionRewrite.Models) != 0 {
		t.Errorf("VisionRewrite.Models = %v; want empty (default)", cfg.VisionRewrite.Models)
	}

	if cfg.VisionRewrite.LogRewrites {
		t.Errorf("VisionRewrite.LogRewrites = true; want false (default)")
	}
}
