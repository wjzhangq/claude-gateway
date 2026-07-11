package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes body to a temp config.yaml and returns its path.
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// minimalHead is the smallest set of fields that passes validate().
const minimalHead = `
server:
  port: 8080
auth:
  session_secret: "s3cr3t"
backends:
  - name: b1
    url: "http://127.0.0.1:9000"
    api_key: "k"
    weight: 1
    enabled: true
`

func TestAnalyzeConfig_Defaults(t *testing.T) {
	// No analyze block at all → every field should fall back to the built-in default.
	p := writeTempConfig(t, minimalHead)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	a := cfg.Analyze
	if a.HaikuBaseURL != "" {
		t.Errorf("HaikuBaseURL = %q, want empty default (use backend node directly)", a.HaikuBaseURL)
	}
	if a.HaikuModel != "claude-haiku-4-5-20251001" {
		t.Errorf("HaikuModel = %q, want default", a.HaikuModel)
	}
	if a.AnalyzerUA != "claude-gateway-analyzer" {
		t.Errorf("AnalyzerUA = %q, want default", a.AnalyzerUA)
	}
	if a.BatchSize != 500 {
		t.Errorf("BatchSize = %d, want 500", a.BatchSize)
	}
	if a.MaxRetry != 3 {
		t.Errorf("MaxRetry = %d, want 3", a.MaxRetry)
	}
	if a.Score.NonWork != 0.7 || a.Score.Volume != 0.3 {
		t.Errorf("Score weights = %+v, want defaults", a.Score)
	}
	if a.Score.BaselineTasks != 60 || a.Score.Threshold != 0.5 {
		t.Errorf("Score baseline/threshold = %+v, want defaults", a.Score)
	}
	if a.Enabled {
		t.Errorf("Enabled = true, want false by default")
	}
}

func TestAnalyzeConfig_Unmarshal(t *testing.T) {
	// A fully-specified analyze block must override every default.
	body := minimalHead + `
analyze:
  enabled: true
  haiku_base_url: "http://example:9999"
  haiku_api_key: "internal-key"
  haiku_model: "claude-haiku-custom"
  analyzer_ua: "my-analyzer"
  batch_size: 100
  max_retry: 5
  score:
    non_work: 0.7
    volume: 0.1
    baseline_tasks: 40
    threshold: 0.8
`
	p := writeTempConfig(t, body)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	a := cfg.Analyze
	if !a.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if a.HaikuBaseURL != "http://example:9999" {
		t.Errorf("HaikuBaseURL = %q", a.HaikuBaseURL)
	}
	if a.HaikuAPIKey != "internal-key" {
		t.Errorf("HaikuAPIKey = %q", a.HaikuAPIKey)
	}
	if a.HaikuModel != "claude-haiku-custom" {
		t.Errorf("HaikuModel = %q", a.HaikuModel)
	}
	if a.AnalyzerUA != "my-analyzer" {
		t.Errorf("AnalyzerUA = %q", a.AnalyzerUA)
	}
	if a.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want 100", a.BatchSize)
	}
	if a.MaxRetry != 5 {
		t.Errorf("MaxRetry = %d, want 5", a.MaxRetry)
	}
	if a.Score.NonWork != 0.7 || a.Score.Volume != 0.1 {
		t.Errorf("Score weights = %+v", a.Score)
	}
	if a.Score.BaselineTasks != 40 || a.Score.Threshold != 0.8 {
		t.Errorf("Score baseline/threshold = %+v", a.Score)
	}
}
