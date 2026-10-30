package db

import (
	"testing"
)

// seedListUser inserts a user and returns its id.
func seedListUser(t *testing.T, d *DB, itcode string) int64 {
	t.Helper()
	res, err := d.Exec(`INSERT INTO users (itcode, name, role, status) VALUES (?, ?, 'user', 'active')`, itcode, itcode)
	if err != nil {
		t.Fatalf("seed user %s: %v", itcode, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedAWSUsageLog(t *testing.T, d *DB, userID int64, cost float64, ts string) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO aws_usage_logs
		(user_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens, cost_usd, created_at)
		VALUES (?, 1, 'opus', 'bedrock-opus', 10, 5, 15, ?, ?)`, userID, cost, ts)
	if err != nil {
		t.Fatalf("seed aws_usage_log: %v", err)
	}
}

// findUser returns the row for an itcode out of a result page.
func findUser(users []*UserWithStats, itcode string) *UserWithStats {
	for _, u := range users {
		if u.Itcode == itcode {
			return u
		}
	}
	return nil
}

// buildListFixture seeds three users with known usage and rolls it up.
//
//	alice: 3 backend requests, $7 total of which $6 OpenClaw, plus $2.50 AWS
//	bob:   1 backend request, $0.50, no OpenClaw, no AWS
//	carol: no usage at all
func buildListFixture(t *testing.T, d *DB) (alice, bob, carol int64) {
	t.Helper()
	alice = seedListUser(t, d, "alice")
	bob = seedListUser(t, d, "bob")
	carol = seedListUser(t, d, "carol")

	seedUsageLog(t, d, alice, "opus", 1.00, 0, "2026-03-01 08:00:00.000000000 +0800 CST")
	seedUsageLog(t, d, alice, "opus", 2.00, 1, "2026-03-01 12:00:00.000000000 +0800 CST")
	seedUsageLog(t, d, alice, "opus", 4.00, 1, "2026-03-02 19:30:00.000000000 +0800 CST")
	seedUsageLog(t, d, bob, "sonnet", 0.50, 0, "2026-03-01 09:00:00.000000000 +0800 CST")

	seedAWSUsageLog(t, d, alice, 2.50, "2026-03-01 10:00:00.000000000 +0800 CST")

	for _, date := range []string{"2026-03-01", "2026-03-02"} {
		if err := d.aggregateForDate(date); err != nil {
			t.Fatalf("aggregateForDate %s: %v", date, err)
		}
		if err := d.aggregateAWSForDate(date); err != nil {
			t.Fatalf("aggregateAWSForDate %s: %v", date, err)
		}
	}
	return alice, bob, carol
}

// TestListUsersWithStatsAggregates is the core correctness check: the numbers
// served from daily_stats must match what the old usage_logs JOIN produced.
func TestListUsersWithStatsAggregates(t *testing.T) {
	d := newTestDB(t)
	buildListFixture(t, d)

	users, total, err := d.ListUsersWithStats(1, 20, "", "id", "asc")
	if err != nil {
		t.Fatalf("ListUsersWithStats: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}

	a := findUser(users, "alice")
	if a == nil {
		t.Fatal("alice missing from result")
	}
	if a.Requests != 3 {
		t.Errorf("alice requests = %d, want 3", a.Requests)
	}
	if a.CostUSD != 7.00 {
		t.Errorf("alice cost_usd = %v, want 7", a.CostUSD)
	}
	if a.OCCostUSD != 6.00 {
		t.Errorf("alice oc_cost_usd = %v, want 6", a.OCCostUSD)
	}
	if a.BackendCostUSD != 7.00 {
		t.Errorf("alice backend_cost_usd = %v, want 7 (same basis as cost_usd)", a.BackendCostUSD)
	}
	if a.AWSCostUSD != 2.50 {
		t.Errorf("alice aws_cost_usd = %v, want 2.5", a.AWSCostUSD)
	}
	if a.LastUsedAt == nil {
		t.Error("alice last_used_at is nil, want the 2026-03-02 19:30 timestamp")
	} else if got := a.LastUsedAt.Format("2006-01-02 15:04"); got != "2026-03-02 19:30" {
		t.Errorf("alice last_used_at = %s, want 2026-03-02 19:30", got)
	}

	b := findUser(users, "bob")
	if b == nil {
		t.Fatal("bob missing from result")
	}
	if b.Requests != 1 || b.CostUSD != 0.50 || b.OCCostUSD != 0 || b.AWSCostUSD != 0 {
		t.Errorf("bob = {req:%d cost:%v oc:%v aws:%v}, want {1 0.5 0 0}",
			b.Requests, b.CostUSD, b.OCCostUSD, b.AWSCostUSD)
	}
}

// TestListUsersWithStatsZeroUsageUser makes sure a user with no usage still
// appears (the LEFT JOINs must not drop them) with zeroed stats.
func TestListUsersWithStatsZeroUsageUser(t *testing.T) {
	d := newTestDB(t)
	buildListFixture(t, d)

	users, _, err := d.ListUsersWithStats(1, 20, "", "id", "asc")
	if err != nil {
		t.Fatalf("ListUsersWithStats: %v", err)
	}

	c := findUser(users, "carol")
	if c == nil {
		t.Fatal("carol (no usage) dropped from result")
	}
	if c.Requests != 0 || c.CostUSD != 0 || c.OCCostUSD != 0 || c.AWSCostUSD != 0 {
		t.Errorf("carol = {req:%d cost:%v oc:%v aws:%v}, want all zero",
			c.Requests, c.CostUSD, c.OCCostUSD, c.AWSCostUSD)
	}
	if c.LastUsedAt != nil {
		t.Errorf("carol last_used_at = %v, want nil for a never-used account", c.LastUsedAt)
	}
}

// TestListUsersWithStatsExcludesSynced pins the agreed cost basis: the imported
// model='synced' rows from the old system carry real cost but requests=0, and
// must not inflate what the user list shows.
func TestListUsersWithStatsExcludesSynced(t *testing.T) {
	d := newTestDB(t)
	alice, _, _ := buildListFixture(t, d)

	if _, err := d.Exec(`INSERT INTO daily_stats (date, user_id, model, requests, input_tokens,
		output_tokens, total_tokens, cost_usd) VALUES ('2026-01-05', ?, 'synced', 0, 0, 0, 0, 500.00)`,
		alice); err != nil {
		t.Fatalf("seed synced row: %v", err)
	}

	users, _, err := d.ListUsersWithStats(1, 20, "", "id", "asc")
	if err != nil {
		t.Fatalf("ListUsersWithStats: %v", err)
	}
	a := findUser(users, "alice")
	if a == nil {
		t.Fatal("alice missing")
	}
	if a.CostUSD != 7.00 {
		t.Errorf("alice cost_usd = %v, want 7 (synced row must be excluded)", a.CostUSD)
	}
}

// TestListUsersWithStatsSorting walks every sortBy the handler accepts, in both
// directions, plus the fallback for an unknown key.
func TestListUsersWithStatsSorting(t *testing.T) {
	d := newTestDB(t)
	buildListFixture(t, d)

	cases := []struct {
		sortBy    string
		sortOrder string
		wantFirst string
	}{
		{"cost_usd", "desc", "alice"},
		{"cost_usd", "asc", "carol"},
		{"backend_cost_usd", "desc", "alice"},
		{"aws_cost_usd", "desc", "alice"},
		{"last_used_at", "desc", "alice"},
		{"itcode", "asc", "alice"},
		{"itcode", "desc", "carol"},
		{"id", "asc", "alice"},
		{"id", "desc", "carol"},
		{"created_at", "asc", "alice"},
		// Unknown key must fall back to u.id rather than error or inject SQL.
		{"'; DROP TABLE users; --", "desc", "carol"},
	}

	for _, tc := range cases {
		users, _, err := d.ListUsersWithStats(1, 20, "", tc.sortBy, tc.sortOrder)
		if err != nil {
			t.Errorf("sortBy=%q order=%q: %v", tc.sortBy, tc.sortOrder, err)
			continue
		}
		if len(users) == 0 {
			t.Errorf("sortBy=%q order=%q: no rows", tc.sortBy, tc.sortOrder)
			continue
		}
		if users[0].Itcode != tc.wantFirst {
			t.Errorf("sortBy=%q order=%q: first = %s, want %s",
				tc.sortBy, tc.sortOrder, users[0].Itcode, tc.wantFirst)
		}
	}

	// The injection attempt above must not have dropped anything.
	var n int
	if err := d.QueryRowRead(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("users table gone: %v", err)
	}
	if n != 3 {
		t.Errorf("users count = %d, want 3", n)
	}
}

// TestListUsersWithStatsSearchAndPaging covers the itcode filter and that total
// reflects the filter rather than the whole table.
func TestListUsersWithStatsSearchAndPaging(t *testing.T) {
	d := newTestDB(t)
	buildListFixture(t, d)

	users, total, err := d.ListUsersWithStats(1, 20, "ali", "id", "asc")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(users) != 1 || users[0].Itcode != "alice" {
		t.Errorf("search %q: total=%d rows=%d, want 1 row alice", "ali", total, len(users))
	}
	// Stats must survive the filtered path too.
	if users[0].CostUSD != 7.00 {
		t.Errorf("filtered alice cost_usd = %v, want 7", users[0].CostUSD)
	}

	// Page 2 of a 2-per-page listing sorted by id asc → the third user.
	page2, total, err := d.ListUsersWithStats(2, 2, "", "id", "asc")
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if total != 3 {
		t.Errorf("page 2 total = %d, want 3", total)
	}
	if len(page2) != 1 || page2[0].Itcode != "carol" {
		t.Errorf("page 2 = %d rows (first %v), want 1 row carol", len(page2), page2)
	}
}

// TestListUsersWithStatsMatchesUsageLogs is the cost-basis regression guard: the
// daily_stats-backed result must equal a direct aggregate over usage_logs.
func TestListUsersWithStatsMatchesUsageLogs(t *testing.T) {
	d := newTestDB(t)
	buildListFixture(t, d)

	users, _, err := d.ListUsersWithStats(1, 50, "", "id", "asc")
	if err != nil {
		t.Fatalf("ListUsersWithStats: %v", err)
	}

	for _, u := range users {
		var wantReq int64
		var wantCost, wantOC float64
		err := d.QueryRowRead(`SELECT COUNT(*), COALESCE(SUM(cost_usd),0),
			COALESCE(SUM(CASE WHEN is_openclaw=1 THEN cost_usd ELSE 0 END),0)
			FROM usage_logs WHERE user_id = ?`, u.ID).Scan(&wantReq, &wantCost, &wantOC)
		if err != nil {
			t.Fatalf("usage_logs aggregate for %s: %v", u.Itcode, err)
		}
		if u.Requests != wantReq {
			t.Errorf("%s requests = %d, usage_logs says %d", u.Itcode, u.Requests, wantReq)
		}
		if u.CostUSD != wantCost {
			t.Errorf("%s cost_usd = %v, usage_logs says %v", u.Itcode, u.CostUSD, wantCost)
		}
		if u.OCCostUSD != wantOC {
			t.Errorf("%s oc_cost_usd = %v, usage_logs says %v", u.Itcode, u.OCCostUSD, wantOC)
		}
	}
}
