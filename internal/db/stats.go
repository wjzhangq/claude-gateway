package db

import (
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// InsertUsageLog writes a single usage record to the database.
func (d *DB) InsertUsageLog(log *model.UsageLog) error {
	_, err := d.Exec(
		`INSERT INTO usage_logs
		 (user_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.UserID, log.APIKeyID, log.Model, log.Backend,
		log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.CostUSD, log.StatusCode, log.Latency,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("insert usage log: %w", err)
	}
	return nil
}

// ListUsageLogs queries usage logs with optional filters.
func (d *DB) ListUsageLogs(userID int64, startDate, endDate, modelFilter string, page, pageSize int) ([]*model.UsageLog, int, error) {
	where := "WHERE 1=1"
	args := []interface{}{}

	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}
	if startDate != "" {
		where += " AND created_at >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		where += " AND created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	if modelFilter != "" {
		where += " AND model = ?"
		args = append(args, modelFilter)
	}

	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM usage_logs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	args = append(args, pageSize, offset)

	rows, err := d.Query(
		`SELECT id, user_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, created_at
		 FROM usage_logs `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.APIKeyID, &l.Model, &l.Backend,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CostUSD,
			&l.StatusCode, &l.Latency, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}
