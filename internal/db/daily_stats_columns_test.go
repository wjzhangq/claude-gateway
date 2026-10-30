package db

import (
	"path/filepath"
	"testing"
	"time"
)

// seedUsageLog inserts one usage_logs row at an explicit timestamp.
func seedUsageLog(t *testing.T, d *DB, userID int64, model string, cost float64, isOpenClaw int, ts string) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO usage_logs
		(user_id, api_key_id, model, input_tokens, output_tokens, total_tokens, cost_usd, is_openclaw, created_at)
		VALUES (?, 1, ?, 10, 5, 15, ?, ?, ?)`, userID, model, cost, isOpenClaw, ts)
	if err != nil {
		t.Fatalf("seed usage_log: %v", err)
	}
}

// TestAggregateForDateNewColumns covers the two columns the user list needs but
// daily_stats did not carry: oc_cost_usd (OpenClaw share) and last_used_at.
func TestAggregateForDateNewColumns(t *testing.T) {
	d := newTestDB(t)
	const date = "2026-03-01"

	// Same user+model, mixed is_openclaw, three timestamps.
	seedUsageLog(t, d, 1, "opus", 1.00, 0, date+" 08:00:00.000000000 +0800 CST")
	seedUsageLog(t, d, 1, "opus", 2.00, 1, date+" 12:00:00.000000000 +0800 CST")
	seedUsageLog(t, d, 1, "opus", 4.00, 1, date+" 19:30:00.000000000 +0800 CST")
	// Different model, no OpenClaw.
	seedUsageLog(t, d, 1, "sonnet", 8.00, 0, date+" 09:00:00.000000000 +0800 CST")

	if err := d.aggregateForDate(date); err != nil {
		t.Fatalf("aggregateForDate: %v", err)
	}

	var requests int
	var cost, ocCost float64
	var lastUsed string
	err := d.QueryRowRead(`SELECT requests, cost_usd, oc_cost_usd, last_used_at
		FROM daily_stats WHERE date=? AND user_id=1 AND model='opus'`, date).
		Scan(&requests, &cost, &ocCost, &lastUsed)
	if err != nil {
		t.Fatalf("read daily_stats: %v", err)
	}

	if requests != 3 {
		t.Errorf("requests = %d, want 3", requests)
	}
	if cost != 7.00 {
		t.Errorf("cost_usd = %v, want 7", cost)
	}
	if ocCost != 6.00 {
		t.Errorf("oc_cost_usd = %v, want 6 (only is_openclaw=1 rows)", ocCost)
	}
	if lastUsed != date+" 19:30:00.000000000 +0800 CST" {
		t.Errorf("last_used_at = %q, want the 19:30 row", lastUsed)
	}

	// The non-OpenClaw model must report zero OpenClaw cost, not NULL.
	var sonnetOC float64
	if err := d.QueryRowRead(`SELECT oc_cost_usd FROM daily_stats
		WHERE date=? AND user_id=1 AND model='sonnet'`, date).Scan(&sonnetOC); err != nil {
		t.Fatalf("read sonnet row: %v", err)
	}
	if sonnetOC != 0 {
		t.Errorf("sonnet oc_cost_usd = %v, want 0", sonnetOC)
	}
}

// TestAggregateForDateIdempotent guards the upsert: re-running a date must
// overwrite, never accumulate. The ON CONFLICT clause has to list the new
// columns too, which is easy to miss.
func TestAggregateForDateIdempotent(t *testing.T) {
	d := newTestDB(t)
	const date = "2026-03-02"
	seedUsageLog(t, d, 7, "opus", 3.00, 1, date+" 10:00:00.000000000 +0800 CST")

	for i := 0; i < 3; i++ {
		if err := d.aggregateForDate(date); err != nil {
			t.Fatalf("aggregateForDate run %d: %v", i, err)
		}
	}

	var requests int
	var cost, ocCost float64
	if err := d.QueryRowRead(`SELECT requests, cost_usd, oc_cost_usd FROM daily_stats
		WHERE date=? AND user_id=7`, date).Scan(&requests, &cost, &ocCost); err != nil {
		t.Fatalf("read daily_stats: %v", err)
	}
	if requests != 1 || cost != 3.00 || ocCost != 3.00 {
		t.Errorf("after 3 runs: requests=%d cost=%v oc=%v, want 1 / 3 / 3", requests, cost, ocCost)
	}
}

// TestAggregateForDateMidnightBoundary checks a row in the last millisecond of a
// day is still counted. The old upper bound was created_at <= 'date 23:59:59.999',
// which drops nanosecond timestamps beyond .999 by string comparison.
func TestAggregateForDateMidnightBoundary(t *testing.T) {
	d := newTestDB(t)
	const date = "2026-03-03"
	seedUsageLog(t, d, 9, "opus", 5.00, 0, date+" 23:59:59.999999999 +0800 CST")
	// Next day, must not leak in.
	seedUsageLog(t, d, 9, "opus", 99.00, 0, "2026-03-04 00:00:00.000000000 +0800 CST")

	if err := d.aggregateForDate(date); err != nil {
		t.Fatalf("aggregateForDate: %v", err)
	}

	var requests int
	var cost float64
	if err := d.QueryRowRead(`SELECT requests, cost_usd FROM daily_stats
		WHERE date=? AND user_id=9`, date).Scan(&requests, &cost); err != nil {
		t.Fatalf("read daily_stats: %v", err)
	}
	if requests != 1 || cost != 5.00 {
		t.Errorf("requests=%d cost=%v, want 1 / 5 (boundary row counted, next day excluded)", requests, cost)
	}
}

// TestAggregateSinceLastRollup covers the gap AggregateDaily leaves: it only
// touches today and yesterday, so any date the server was down for stays
// un-aggregated forever.
func TestAggregateSinceLastRollup(t *testing.T) {
	d := newTestDB(t)

	now := time.Now()
	// Dates 5, 4, 3 days back — all outside AggregateDaily's today/yesterday window.
	var dates []string
	for i := 5; i >= 3; i-- {
		day := now.AddDate(0, 0, -i)
		date := day.Format("2006-01-02")
		dates = append(dates, date)
		seedUsageLog(t, d, 3, "opus", 1.00, 0, date+" 10:00:00.000000000 +0800 CST")
	}

	// Simulate an existing rollup for the oldest date only, as if the server went
	// down right after it.
	if err := d.aggregateForDate(dates[0]); err != nil {
		t.Fatalf("seed rollup: %v", err)
	}

	var before int
	d.QueryRowRead(`SELECT COUNT(DISTINCT date) FROM daily_stats`).Scan(&before)
	if before != 1 {
		t.Fatalf("precondition: %d distinct dates, want 1", before)
	}

	if err := d.AggregateSinceLastRollup(); err != nil {
		t.Fatalf("AggregateSinceLastRollup: %v", err)
	}

	for _, date := range dates {
		var requests int
		var cost float64
		err := d.QueryRowRead(`SELECT requests, cost_usd FROM daily_stats
			WHERE date=? AND user_id=3`, date).Scan(&requests, &cost)
		if err != nil {
			t.Errorf("date %s not aggregated: %v", date, err)
			continue
		}
		if requests != 1 || cost != 1.00 {
			t.Errorf("date %s: requests=%d cost=%v, want 1 / 1", date, requests, cost)
		}
	}
}

// TestBackfillMigrationPopulatesHistory verifies migration 56 rebuilt the new
// columns for dates far outside the today/yesterday window, and left the
// imported model='synced' rows from the old system alone.
func TestBackfillMigrationPopulatesHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill.db")

	// First open: schema only, with migration 56 removed from the list so we can
	// seed "pre-existing" history the way production has it.
	d, err := Init(dbPath, 1000)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}

	// Historical usage_logs that no rollup ever covered.
	seedUsageLog(t, d, 11, "opus", 2.50, 1, "2026-01-05 10:00:00.000000000 +0800 CST")
	seedUsageLog(t, d, 11, "opus", 2.50, 0, "2026-01-05 14:00:00.000000000 +0800 CST")
	// A synced row from the old system: requests=0, real cost, must survive untouched.
	if _, err := d.Exec(`INSERT INTO daily_stats (date, user_id, model, requests, input_tokens,
		output_tokens, total_tokens, cost_usd) VALUES ('2026-01-05', 11, 'synced', 0, 0, 0, 0, 77.50)`); err != nil {
		t.Fatalf("seed synced row: %v", err)
	}
	// Drop the backfill marker so reopening re-runs it against the seeded data.
	if _, err := d.Exec(`DELETE FROM schema_migrations WHERE version = 56`); err != nil {
		t.Fatalf("reset migration 56: %v", err)
	}
	d.Close()

	// Reopen: migration 56 runs again and backfills the seeded history.
	d2, err := Init(dbPath, 1000)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer d2.Close()

	var requests int
	var cost, ocCost float64
	var lastUsed string
	err = d2.QueryRowRead(`SELECT requests, cost_usd, oc_cost_usd, last_used_at FROM daily_stats
		WHERE date='2026-01-05' AND user_id=11 AND model='opus'`).
		Scan(&requests, &cost, &ocCost, &lastUsed)
	if err != nil {
		t.Fatalf("backfilled row missing: %v", err)
	}
	if requests != 2 || cost != 5.00 || ocCost != 2.50 {
		t.Errorf("backfill: requests=%d cost=%v oc=%v, want 2 / 5 / 2.5", requests, cost, ocCost)
	}
	if lastUsed != "2026-01-05 14:00:00.000000000 +0800 CST" {
		t.Errorf("backfill last_used_at = %q, want the 14:00 row", lastUsed)
	}

	// The synced row keeps its cost and its requests=0 marker.
	var syncedCost float64
	var syncedReq int
	if err := d2.QueryRowRead(`SELECT requests, cost_usd FROM daily_stats
		WHERE date='2026-01-05' AND user_id=11 AND model='synced'`).Scan(&syncedReq, &syncedCost); err != nil {
		t.Fatalf("synced row lost: %v", err)
	}
	if syncedReq != 0 || syncedCost != 77.50 {
		t.Errorf("synced row changed: requests=%d cost=%v, want 0 / 77.5", syncedReq, syncedCost)
	}
}
