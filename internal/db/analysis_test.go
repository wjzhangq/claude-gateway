package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/classify"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// newTestDB opens a fresh migrated SQLite database in a temp dir.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Init(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func countRows(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestPendingEnqueue_OnlySuccessfulUserInitiated verifies the collection-side
// invariants (quickstart scenario 1): a successful user_initiated row with a
// signal is enqueued; a failed row and a continuation row are not.
func TestPendingEnqueue_OnlySuccessfulUserInitiated(t *testing.T) {
	d := newTestDB(t)

	logs := []*model.UsageLog{
		// eligible: success + user_initiated + signal
		{UserID: 1, Model: "m", StatusCode: 200, PendingSignal: `{"intent":"a"}`, PendingUserInitiated: true},
		// failed request: never enqueued (invariant B)
		{UserID: 1, Model: "m", StatusCode: 429, PendingSignal: `{"intent":"b"}`, PendingUserInitiated: true},
		// continuation: not user_initiated
		{UserID: 1, Model: "m", StatusCode: 200, PendingSignal: `{"intent":"c"}`, PendingUserInitiated: false},
		// success but no signal (e.g. analyze disabled): not enqueued
		{UserID: 1, Model: "m", StatusCode: 200, PendingSignal: "", PendingUserInitiated: true},
	}
	if err := d.BatchInsertUsageLogs(logs); err != nil {
		t.Fatalf("batch insert: %v", err)
	}

	if got := countRows(t, d, "usage_logs"); got != 4 {
		t.Errorf("usage_logs rows = %d, want 4 (all requests logged)", got)
	}
	if got := countRows(t, d, "pending_analysis"); got != 1 {
		t.Errorf("pending_analysis rows = %d, want 1 (only the eligible one)", got)
	}

	pend, err := d.ListPending(500, 3)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pend) != 1 {
		t.Fatalf("ListPending len = %d, want 1", len(pend))
	}
	if pend[0].Signal != `{"intent":"a"}` {
		t.Errorf("enqueued signal = %q, want the eligible one", pend[0].Signal)
	}
	// The pending row must point at an existing usage_logs row (no orphan).
	var exists int
	if err := d.QueryRow("SELECT COUNT(*) FROM usage_logs WHERE id=?", pend[0].UsageLogID).Scan(&exists); err != nil {
		t.Fatalf("check usage_log: %v", err)
	}
	if exists != 1 {
		t.Errorf("pending.usage_log_id has no matching usage_logs row")
	}
}

// TestWriteBackResults_UpdateDeleteAndRetry verifies write-back applies the verdict
// and clears the queue, while a retry keeps the row and bumps its counter
// (quickstart scenarios 2, 5, 6).
func TestWriteBackResults_UpdateDeleteAndRetry(t *testing.T) {
	d := newTestDB(t)

	logs := []*model.UsageLog{
		{UserID: 7, Model: "m", StatusCode: 200, PendingSignal: `{"intent":"x"}`, PendingUserInitiated: true},
		{UserID: 7, Model: "m", StatusCode: 200, PendingSignal: `{"intent":"y"}`, PendingUserInitiated: true},
	}
	if err := d.BatchInsertUsageLogs(logs); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	pend, err := d.ListPending(500, 3)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pend) != 2 {
		t.Fatalf("pending len = %d, want 2", len(pend))
	}

	tr := true
	results := []*AnalysisResult{
		// first: full verdict → update + delete
		{PendingID: pend[0].ID, UsageLogID: pend[0].UsageLogID, TaskType: "code", WorkRelated: &tr, CodeDirection: "后端", WorkReason: "命中内部仓库 modelgate"},
		// second: retry → keep, bump counter
		{PendingID: pend[1].ID, UsageLogID: pend[1].UsageLogID, Retry: true},
	}
	counts, err := d.WriteBackResults(results)
	if err != nil {
		t.Fatalf("write back: %v", err)
	}
	if counts.Updated != 1 || counts.Deleted != 1 || counts.Retried != 1 {
		t.Errorf("counts = %+v, want updated=1 deleted=1 retried=1", counts)
	}

	// usage_logs verdict written back with the merged error_reason.
	var taskType, codeDir, reason string
	var workRelated int
	if err := d.QueryRow(
		"SELECT task_type, work_related, code_direction, error_reason FROM usage_logs WHERE id=?",
		pend[0].UsageLogID).Scan(&taskType, &workRelated, &codeDir, &reason); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if taskType != "code" || workRelated != 1 || codeDir != "后端" {
		t.Errorf("verdict = %s/%d/%s, want code/1/后端", taskType, workRelated, codeDir)
	}
	if reason != "work:命中内部仓库 modelgate" {
		t.Errorf("error_reason = %q, want work: segment", reason)
	}

	// Queue now holds only the retried row, with retry_count = 1.
	if got := countRows(t, d, "pending_analysis"); got != 1 {
		t.Errorf("pending rows = %d, want 1 (retried kept)", got)
	}
	var rc int
	if err := d.QueryRow("SELECT retry_count FROM pending_analysis WHERE id=?", pend[1].ID).Scan(&rc); err != nil {
		t.Fatalf("read retry_count: %v", err)
	}
	if rc != 1 {
		t.Errorf("retry_count = %d, want 1", rc)
	}

	// Idempotency: re-listing returns only the still-pending (retried) row.
	pend2, err := d.ListPending(500, 3)
	if err != nil {
		t.Fatalf("list pending 2: %v", err)
	}
	if len(pend2) != 1 || pend2[0].ID != pend[1].ID {
		t.Errorf("second ListPending = %+v, want only the retried row", pend2)
	}
}

// TestListPending_RespectsRetryCeiling verifies records at/over the retry ceiling
// are skipped (FR-010 skip path).
func TestListPending_RespectsRetryCeiling(t *testing.T) {
	d := newTestDB(t)
	logs := []*model.UsageLog{
		{UserID: 1, Model: "m", StatusCode: 200, PendingSignal: `{"intent":"z"}`, PendingUserInitiated: true},
	}
	if err := d.BatchInsertUsageLogs(logs); err != nil {
		t.Fatalf("insert: %v", err)
	}
	pend, _ := d.ListPending(500, 3)
	// Retry it up to the ceiling.
	for i := 0; i < 3; i++ {
		if _, err := d.WriteBackResults([]*AnalysisResult{{PendingID: pend[0].ID, UsageLogID: pend[0].UsageLogID, Retry: true}}); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	// retry_count is now 3; with maxRetry=3 it must be excluded.
	got, err := d.ListPending(500, 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListPending len = %d, want 0 (over retry ceiling)", len(got))
	}
}

// TestListTaggedRecords_FeedsAggregation verifies the aggregation read path only
// returns successful, tagged rows and that the work: reason round-trips for
// non-work sampling (quickstart scenario 7 data path).
func TestListTaggedRecords_FeedsAggregation(t *testing.T) {
	d := newTestDB(t)

	// Insert tagged rows directly (simulating post-write-back state).
	now := time.Now()
	insert := func(user int64, status int, taskType string, work int, dir, reason string) {
		_, err := d.Exec(
			`INSERT INTO usage_logs (user_id, api_key_id, model, status_code, task_type, work_related, code_direction, error_reason, created_at)
			 VALUES (?, 0, 'm', ?, ?, ?, ?, ?, ?)`,
			user, status, taskType, work, dir, reason, now)
		if err != nil {
			t.Fatalf("insert tagged: %v", err)
		}
	}
	insert(9, 200, "code", 1, "后端", "work:命中内部仓库 x")
	insert(9, 200, "other", 0, "", "work:个人项目")
	insert(9, 200, "", -1, "", "")   // untagged → excluded
	insert(9, 500, "code", 1, "后端", "") // failed → excluded

	recs, err := d.ListTaggedRecords(9, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("list tagged: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("tagged records = %d, want 2 (only successful+tagged)", len(recs))
	}

	cfg := classify.DefaultConfig()
	roll := classify.Aggregate(recs, now.Add(-time.Hour), cfg)
	if roll.LogicalTasks != 2 {
		t.Errorf("LogicalTasks = %d, want 2", roll.LogicalTasks)
	}
	if roll.WorkTasks != 1 || roll.NonWorkTasks != 1 {
		t.Errorf("work/non-work = %d/%d, want 1/1", roll.WorkTasks, roll.NonWorkTasks)
	}
	// The non-work reason must have round-tripped out of error_reason.
	if len(roll.NonWorkExample) != 1 || roll.NonWorkExample[0] != "个人项目" {
		t.Errorf("NonWorkExample = %v, want [个人项目]", roll.NonWorkExample)
	}
}
