package config

import "testing"

func TestLookupBackendDailyLimit(t *testing.T) {
	cfg := &Config{
		BackendDailyLimits: []BackendDailyLimit{
			{Prefix: "lianxiang-", DailyUSD: 5000},
			{Prefix: "dasheng-", DailyUSD: 400},
			{Prefix: "lianxiang-sc999", DailyUSD: 10}, // more specific prefix
			{Name: "claude-primary", DailyUSD: 100},
			{Name: "unlimited-backend", DailyUSD: 0}, // explicit unlimited
		},
	}

	cases := []struct {
		name string
		want float64
	}{
		{"lianxiang-sc031", 5000},          // prefix match
		{"lianxiang-sc032", 5000},          // prefix match
		{"dasheng-01", 400},                // other prefix
		{"lianxiang-sc999-extra", 10},      // longest prefix wins
		{"claude-primary", 100},            // exact name
		{"unlimited-backend", 0},           // exact name, 0 = unlimited
		{"no-match-backend", 0},            // no entry = unlimited
	}

	for _, tc := range cases {
		if got := cfg.LookupBackendDailyLimit(tc.name); got != tc.want {
			t.Errorf("LookupBackendDailyLimit(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLookupBackendDailyLimitExactBeatsPrefix(t *testing.T) {
	cfg := &Config{
		BackendDailyLimits: []BackendDailyLimit{
			{Prefix: "lianxiang-", DailyUSD: 5000},
			{Name: "lianxiang-sc031", DailyUSD: 250}, // exact override for one backend
		},
	}
	if got := cfg.LookupBackendDailyLimit("lianxiang-sc031"); got != 250 {
		t.Errorf("exact name should win over prefix: got %v, want 250", got)
	}
	if got := cfg.LookupBackendDailyLimit("lianxiang-sc032"); got != 5000 {
		t.Errorf("prefix should still apply to others: got %v, want 5000", got)
	}
}
