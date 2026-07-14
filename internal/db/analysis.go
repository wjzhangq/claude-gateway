package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/classify"
)

// PendingRecord is one row of the to-analyze queue, joined with nothing else.
// Signal is the raw JSON string persisted at collection time (a classify.Signal);
// the analyzer decodes it. Role is not stored — the collector only enqueues
// user_initiated rounds — so callers may treat every pending record as such.
type PendingRecord struct {
	ID         int64     `json:"id"`
	UsageLogID int64     `json:"usage_log_id"`
	UserID     int64     `json:"user_id"`
	Signal     string    `json:"signal"` // classify.Signal JSON
	RetryCount int       `json:"retry_count"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListPending returns up to limit not-yet-analyzed records, skipping those that
// already exhausted their retry budget (retry_count >= maxRetry). newestFirst
// controls the scan order: false (default) returns the oldest queued rows first
// (ORDER BY id ASC) so a full drain processes the backlog in arrival order; true
// returns the most recent rows first (ORDER BY id DESC), used by --analyze --recent
// to sample just the latest batch. The queue table itself is the incremental
// watermark: a successfully written-back row is deleted, so repeated full-drain
// runs only ever see new work (SC-005).
func (d *DB) ListPending(limit, maxRetry int, newestFirst bool) ([]*PendingRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	order := "ORDER BY id ASC"
	if newestFirst {
		order = "ORDER BY id DESC"
	}
	rows, err := d.Query(
		`SELECT id, usage_log_id, user_id, signal, retry_count, created_at
		 FROM pending_analysis
		 WHERE retry_count < ?
		 `+order+`
		 LIMIT ?`, maxRetry, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()

	var out []*PendingRecord
	for rows.Next() {
		p := &PendingRecord{}
		if err := rows.Scan(&p.ID, &p.UsageLogID, &p.UserID, &p.Signal, &p.RetryCount, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AnalysisResult is one verdict to write back.
//   - Retry=true: analysis failed transiently; bump retry_count (kept for
//     backward-compat, but new clients should prefer Delete=true).
//   - Delete=true: analysis failed permanently (corrupt signal, Haiku error);
//     drop the pending row without updating usage_logs.
//   - Neither: success — update usage_logs and delete the pending row.
type AnalysisResult struct {
	PendingID     int64  `json:"pending_id"`
	UsageLogID    int64  `json:"usage_log_id"`
	TaskType      string `json:"task_type"`
	WorkRelated   *bool  `json:"work_related"`
	CodeDirection string `json:"code_direction"`
	WorkReason    string `json:"work_reason"`
	DocActivity   string `json:"doc_activity"`
	FromHaiku     bool   `json:"from_haiku"`
	Retry         bool   `json:"retry"`
	Delete        bool   `json:"delete"`
}

// WriteBackCounts summarizes one WriteBackResults call.
type WriteBackCounts struct {
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	Retried int `json:"retried"`
	Purged  int `json:"purged"`
}

// WriteBackResults applies each verdict inside a single transaction:
//   - retry=false: UPDATE the target usage_logs row with the verdict, then DELETE
//     the pending_analysis row (the record is done).
//   - retry=true:  leave usage_logs untouched and bump the pending row's
//     retry_count so it is retried on a later run (FR-010).
//
// work_related maps *bool → {1,0}; a nil pointer writes -1 (undetermined).
func (d *DB) WriteBackResults(results []*AnalysisResult) (WriteBackCounts, error) {
	var counts WriteBackCounts
	if len(results) == 0 {
		return counts, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return counts, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() // 兜底：Commit 成功后 no-op；错误路径清理事务

	updateStmt, err := tx.PrepareContext(ctx,
		`UPDATE usage_logs
		 SET task_type=?, work_related=?, code_direction=?, error_reason=?
		 WHERE id=?`)
	if err != nil {
		return counts, fmt.Errorf("prepare update: %w", err)
	}
	defer updateStmt.Close()
	deleteStmt, err := tx.PrepareContext(ctx, `DELETE FROM pending_analysis WHERE id=?`)
	if err != nil {
		return counts, fmt.Errorf("prepare delete: %w", err)
	}
	defer deleteStmt.Close()
	retryStmt, err := tx.PrepareContext(ctx, `UPDATE pending_analysis SET retry_count=retry_count+1 WHERE id=?`)
	if err != nil {
		return counts, fmt.Errorf("prepare retry: %w", err)
	}
	defer retryStmt.Close()

	for _, r := range results {
		if r.Delete {
			if _, err := deleteStmt.ExecContext(ctx, r.PendingID); err != nil {
				return counts, fmt.Errorf("exec delete (failure): %w", err)
			}
			counts.Purged++
			continue
		}
		if r.Retry {
			if _, err := retryStmt.ExecContext(ctx, r.PendingID); err != nil {
				return counts, fmt.Errorf("exec retry: %w", err)
			}
			counts.Retried++
			continue
		}
		wr := -1
		if r.WorkRelated != nil {
			if *r.WorkRelated {
				wr = 1
			} else {
				wr = 0
			}
		}
		if _, err := updateStmt.ExecContext(ctx,
			r.TaskType, wr, r.CodeDirection, mergeReason(r.WorkReason, r.DocActivity), r.UsageLogID,
		); err != nil {
			return counts, fmt.Errorf("exec update: %w", err)
		}
		counts.Updated++
		if _, err := deleteStmt.ExecContext(ctx, r.PendingID); err != nil {
			return counts, fmt.Errorf("exec delete: %w", err)
		}
		counts.Deleted++
	}
	if err := tx.Commit(); err != nil {
		return WriteBackCounts{}, err
	}
	return counts, nil
}

// mergeReason packs work_reason and doc_activity into the reused error_reason
// column as "work:<reason>;doc:<activity>", omitting either empty segment.
func mergeReason(workReason, docActivity string) string {
	var parts []string
	if workReason != "" {
		parts = append(parts, "work:"+workReason)
	}
	if docActivity != "" {
		parts = append(parts, "doc:"+docActivity)
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ";"
		}
		out += p
	}
	return out
}

// ListTaggedRecords reads back already-tagged usage_logs rows for aggregation:
// successful (status<400) rows in [windowStart, now] that carry a non-empty
// task_type (i.e. a logical task the analyzer has classified). The work: segment
// of error_reason is parsed back out for non-work sampling. Rows are returned in
// ascending created_at order.
func (d *DB) ListTaggedRecords(userID int64, windowStart time.Time) ([]classify.Record, error) {
	where := "WHERE status_code < 400 AND task_type != '' AND created_at >= ?"
	args := []interface{}{windowStart}
	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}
	rows, err := d.Query(
		`SELECT user_id, created_at, task_type, work_related, code_direction, error_reason
		 FROM usage_logs `+where+` ORDER BY created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list tagged records: %w", err)
	}
	defer rows.Close()

	var out []classify.Record
	for rows.Next() {
		var rec classify.Record
		var reason string
		if err := rows.Scan(&rec.UserID, &rec.CreatedAt, &rec.TaskType, &rec.WorkRelated, &rec.CodeDirection, &reason); err != nil {
			return nil, err
		}
		rec.WorkReason = parseWorkReason(reason)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeletePendingBefore deletes all pending_analysis rows with id < maxID and
// returns the number of rows removed. Used by --recent to evict older records
// (and their stored user-intent signals) after the fresh batch is processed.
func (d *DB) DeletePendingBefore(maxID int64) (int64, error) {
	res, err := d.Exec(`DELETE FROM pending_analysis WHERE id < ?`, maxID)
	if err != nil {
		return 0, fmt.Errorf("delete pending before %d: %w", maxID, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// parseWorkReason pulls the "work:<reason>" segment back out of the reused
// error_reason column (format "work:<reason>;doc:<activity>"), returning just the
// reason text (or "" when absent).
func parseWorkReason(reason string) string {
	for _, seg := range strings.Split(reason, ";") {
		if strings.HasPrefix(seg, "work:") {
			return strings.TrimPrefix(seg, "work:")
		}
	}
	return ""
}
