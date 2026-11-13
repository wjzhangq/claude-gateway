package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a minimal valid config with the given body appended and
// returns its path. The fixed header satisfies validate()'s unrelated checks.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	const header = `
server:
  port: 8080
auth:
  session_secret: "test-secret"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(header+body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestBackendGroupsExpand(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
backend_groups:
  - host: https://one.example.com
    name_prefix: alpha
    keys:
      - sk-a1
      - sk-a2
      - sk-a3
    weight_per_key: 10
  - host: https://two.example.com
    name_prefix: beta
    keys:
      - sk-b1
    weight_per_key: 25
    quota_reset: cst-midnight
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := len(cfg.Backends); got != 4 {
		t.Fatalf("expanded to %d backends, want 4", got)
	}

	// Names are prefix-NNN in key order, starting at 001.
	wantNames := []string{"alpha-001", "alpha-002", "alpha-003", "beta-001"}
	for i, want := range wantNames {
		if got := cfg.Backends[i].Name; got != want {
			t.Errorf("Backends[%d].Name = %q, want %q", i, got, want)
		}
	}

	// Host, key, weight and quota_reset must reach every expanded entry.
	for i := 0; i < 3; i++ {
		b := cfg.Backends[i]
		if b.URL != "https://one.example.com" {
			t.Errorf("Backends[%d].URL = %q", i, b.URL)
		}
		if b.Weight != 10 {
			t.Errorf("Backends[%d].Weight = %d, want 10", i, b.Weight)
		}
		if !b.Enabled {
			t.Errorf("Backends[%d] should be enabled by default", i)
		}
		if b.QuotaReset != "" {
			t.Errorf("Backends[%d].QuotaReset = %q, want empty", i, b.QuotaReset)
		}
	}
	if got := cfg.Backends[0].APIKey; got != "sk-a1" {
		t.Errorf("Backends[0].APIKey = %q, want sk-a1", got)
	}
	if got := cfg.Backends[3].QuotaReset; got != "cst-midnight" {
		t.Errorf("Backends[3].QuotaReset = %q, want cst-midnight", got)
	}
	if got := cfg.Backends[3].Weight; got != 25 {
		t.Errorf("Backends[3].Weight = %d, want 25", got)
	}
}

// The group form and the long form must produce the same backend set, so a
// migration cannot change routing behaviour.
func TestBackendGroupsEquivalentToLongForm(t *testing.T) {
	grouped, err := Load(writeConfig(t, `
backend_groups:
  - host: https://h.example.com
    name_prefix: node
    keys: [sk-1, sk-2]
    weight_per_key: 10
`))
	if err != nil {
		t.Fatalf("Load grouped: %v", err)
	}

	longForm, err := Load(writeConfig(t, `
backends:
  - name: node-001
    url: https://h.example.com
    api_key: sk-1
    weight: 10
    enabled: true
  - name: node-002
    url: https://h.example.com
    api_key: sk-2
    weight: 10
    enabled: true
`))
	if err != nil {
		t.Fatalf("Load long form: %v", err)
	}

	if len(grouped.Backends) != len(longForm.Backends) {
		t.Fatalf("grouped has %d backends, long form has %d",
			len(grouped.Backends), len(longForm.Backends))
	}
	for i := range grouped.Backends {
		if grouped.Backends[i] != longForm.Backends[i] {
			t.Errorf("Backends[%d]:\n grouped = %+v\nlongForm = %+v",
				i, grouped.Backends[i], longForm.Backends[i])
		}
	}
}

// A parked key keeps its slot in the numbering, so disabling one key does not
// renumber the others and silently move their quota.
func TestBackendGroupsDisabledKeyKeepsNumbering(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
backend_groups:
  - host: https://h.example.com
    name_prefix: node
    keys: [sk-1, sk-2, sk-3]
    weight_per_key: 10
    disabled: [sk-2]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Backends) != 3 {
		t.Fatalf("got %d backends, want 3 (disabled keys still expand)", len(cfg.Backends))
	}
	for i, want := range []bool{true, false, true} {
		if got := cfg.Backends[i].Enabled; got != want {
			t.Errorf("Backends[%d] (%s) Enabled = %v, want %v",
				i, cfg.Backends[i].Name, got, want)
		}
	}
	if got := cfg.Backends[2].Name; got != "node-003" {
		t.Errorf("third backend is named %q; disabling sk-2 must not renumber", got)
	}
}

// Groups append to hand-written entries so both forms can coexist mid-migration.
func TestBackendGroupsCoexistWithLongForm(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
backends:
  - name: legacy-01
    url: https://legacy.example.com
    api_key: sk-legacy
    weight: 5
    enabled: true
backend_groups:
  - host: https://new.example.com
    name_prefix: node
    keys: [sk-1]
    weight_per_key: 10
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Backends) != 2 {
		t.Fatalf("got %d backends, want 2", len(cfg.Backends))
	}
	if cfg.Backends[0].Name != "legacy-01" {
		t.Errorf("hand-written entry should come first, got %q", cfg.Backends[0].Name)
	}
	if cfg.Backends[1].Name != "node-001" {
		t.Errorf("group entry should follow, got %q", cfg.Backends[1].Name)
	}
}

// A name collision would make two accounts share health state and quota
// counters, so it has to fail loudly at load rather than at runtime.
func TestBackendNameCollisionRejected(t *testing.T) {
	_, err := Load(writeConfig(t, `
backends:
  - name: node-001
    url: https://legacy.example.com
    api_key: sk-legacy
    weight: 5
    enabled: true
backend_groups:
  - host: https://new.example.com
    name_prefix: node
    keys: [sk-1]
    weight_per_key: 10
`))
	if err == nil {
		t.Fatal("a backend name shared by a hand-written entry and a group was accepted")
	}
}

func TestBackendGroupsRejectsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing host",
			body: "backend_groups:\n  - name_prefix: node\n    keys: [sk-1]\n",
		},
		{
			name: "missing name_prefix",
			body: "backend_groups:\n  - host: https://h.example.com\n    keys: [sk-1]\n",
		},
		{
			name: "no keys",
			body: "backend_groups:\n  - host: https://h.example.com\n    name_prefix: node\n    keys: []\n",
		},
		{
			name: "empty key",
			body: "backend_groups:\n  - host: https://h.example.com\n    name_prefix: node\n    keys: [sk-1, \"\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tt.body)); err == nil {
				t.Errorf("%s was accepted; expected an error rather than a shrunken pool", tt.name)
			}
		})
	}
}

// weight_per_key omitted falls through to validate()'s weight>=1 default,
// matching what a hand-written entry without a weight does.
func TestBackendGroupsDefaultWeight(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
backend_groups:
  - host: https://h.example.com
    name_prefix: node
    keys: [sk-1]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Backends[0].Weight; got != 1 {
		t.Errorf("Weight = %d, want 1 (validate's floor)", got)
	}
}

// Per-backend daily limits key off the backend name, so generated names must
// still match a prefix rule in backend_daily_limits.
func TestBackendGroupsQuotaLookupByGeneratedName(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
backend_daily_limits:
  - prefix: dasheng
    backend_daily_usd: 500
backend_groups:
  - host: https://h.example.com
    name_prefix: dasheng
    keys: [sk-1, sk-2]
    weight_per_key: 10
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, b := range cfg.Backends {
		if got := cfg.LookupBackendDailyLimit(b.Name); got != 500 {
			t.Errorf("LookupBackendDailyLimit(%q) = %v, want 500", b.Name, got)
		}
	}
}
