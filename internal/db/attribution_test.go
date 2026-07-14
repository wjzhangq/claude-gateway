package db

import "testing"

func TestFoldAttribution(t *testing.T) {
	rows := []attrRow{
		// shen / Kavin: two members, one active
		{itcode: "a1", name: "A1", side: "shen", group: "Kavin", tokens: 100, cost: 1.5, requests: 10},
		{itcode: "a2", name: "A2", side: "shen", group: "Kavin", tokens: 0, cost: 0, requests: 0},
		// shen / Simon: one active
		{itcode: "b1", name: "B1", side: "shen", group: "Simon", tokens: 300, cost: 3, requests: 5},
		// non / Su: one active
		{itcode: "c1", name: "C1", side: "non", group: "Su", tokens: 50, cost: 0.5, requests: 2},
		// unmatched: consumed but no side
		{itcode: "u1", name: "U1", side: "", group: "", tokens: 20, cost: 0.2, requests: 1},
		// unmatched but zero consumption → dropped from diagnostics
		{itcode: "u2", name: "U2", side: "", group: "", tokens: 0, cost: 0, requests: 0},
		// departed: consumed, excluded from rollups
		{itcode: "d1", name: "D1", side: "shen", group: "Kavin", isDeparted: true, tokens: 999, cost: 9, requests: 3},
	}

	got := foldAttribution(rows, 0, "ASDC", "SMB")

	// Side labels and period.
	if got.Shen.Label != "ASDC" || got.Non.Label != "SMB" {
		t.Fatalf("labels = %q/%q", got.Shen.Label, got.Non.Label)
	}
	if got.Period != "全量" {
		t.Errorf("period = %q, want 全量", got.Period)
	}

	// Shen totals exclude departed d1: 100 + 0 + 300 = 400 tokens.
	if got.Shen.Tokens != 400 {
		t.Errorf("shen tokens = %d, want 400", got.Shen.Tokens)
	}
	if got.Shen.GroupCount != 2 {
		t.Errorf("shen groups = %d, want 2", got.Shen.GroupCount)
	}
	// Groups sorted by tokens desc → Simon (300) before Kavin (100).
	if got.Shen.Groups[0].Leader != "Simon" {
		t.Errorf("shen top group = %q, want Simon", got.Shen.Groups[0].Leader)
	}

	// Kavin: org_count 2, active_count 1 (a2 has zero tokens; d1 excluded as departed).
	var kavin *AttrGroup
	for _, g := range got.Shen.Groups {
		if g.Leader == "Kavin" {
			kavin = g
		}
	}
	if kavin == nil {
		t.Fatal("Kavin group missing")
	}
	if kavin.OrgCount != 2 || kavin.ActiveCount != 1 {
		t.Errorf("Kavin org/active = %d/%d, want 2/1", kavin.OrgCount, kavin.ActiveCount)
	}

	// Non side.
	if got.Non.Tokens != 50 || got.Non.GroupCount != 1 {
		t.Errorf("non tokens/groups = %d/%d, want 50/1", got.Non.Tokens, got.Non.GroupCount)
	}

	// Unmatched: only u1 (u2 has zero consumption).
	if got.UnmatchedTotal != 1 || len(got.Unmatched) != 1 || got.Unmatched[0].Itcode != "u1" {
		t.Errorf("unmatched = %+v (total %d), want [u1]", got.Unmatched, got.UnmatchedTotal)
	}

	// Departed: d1 only, with its tokens/cost rolled up.
	if got.DepartedTotal != 1 || got.DepartedTokens != 999 {
		t.Errorf("departed total/tokens = %d/%d, want 1/999", got.DepartedTotal, got.DepartedTokens)
	}
}

func TestFoldAttributionEmpty(t *testing.T) {
	got := foldAttribution(nil, 7, "S", "N")
	if got.Period != "近7天" {
		t.Errorf("period = %q, want 近7天", got.Period)
	}
	// Slices must be non-nil so JSON serializes as [] not null.
	if got.Shen.Groups == nil || got.Non.Groups == nil || got.Unmatched == nil || got.Departed == nil {
		t.Error("empty slices should be non-nil for clean JSON")
	}
	if got.Shen.Tokens != 0 || got.UnmatchedTotal != 0 {
		t.Error("empty input should yield zero totals")
	}
}
