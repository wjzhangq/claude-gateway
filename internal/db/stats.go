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
	countWhere := "WHERE 1=1"
	joinWhere := "WHERE 1=1"
	args := []interface{}{}

	if userID > 0 {
		countWhere += " AND user_id = ?"
		joinWhere += " AND l.user_id = ?"
		args = append(args, userID)
	}
	if startDate != "" {
		countWhere += " AND created_at >= ?"
		joinWhere += " AND l.created_at >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		countWhere += " AND created_at <= ?"
		joinWhere += " AND l.created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	if modelFilter != "" {
		countWhere += " AND model = ?"
		joinWhere += " AND l.model = ?"
		args = append(args, modelFilter)
	}

	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM usage_logs "+countWhere, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	joinArgs := append(args, pageSize, offset)

	rows, err := d.Query(
		`SELECT l.id, l.user_id, u.itcode, l.api_key_id, l.model, l.backend, l.input_tokens, l.output_tokens, l.total_tokens, l.cost_usd, l.status_code, l.latency_ms, l.created_at
		 FROM usage_logs l LEFT JOIN users u ON u.id = l.user_id `+joinWhere+` ORDER BY l.created_at DESC LIMIT ? OFFSET ?`, joinArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Model, &l.Backend,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CostUSD,
			&l.StatusCode, &l.Latency, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

// BackendStat holds aggregated usage for a single backend.
type BackendStat struct {
	Backend      string  `json:"backend"`
	Requests     int     `json:"requests"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	ErrorCount   int     `json:"error_count"`
}

// GetBackendStats aggregates usage_logs by backend for the given date range.
func (d *DB) GetBackendStats(startDate, endDate string) ([]*BackendStat, error) {
	where := "WHERE backend != ''"
	args := []interface{}{}

	if startDate != "" {
		where += " AND created_at >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		where += " AND created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}

	rows, err := d.Query(
		`SELECT backend,
		        COUNT(*) as requests,
		        SUM(total_tokens) as total_tokens,
		        SUM(cost_usd) as cost_usd,
		        AVG(latency_ms) as avg_latency_ms,
		        SUM(CASE WHEN status_code != 200 THEN 1 ELSE 0 END) as error_count
		 FROM usage_logs `+where+`
		 GROUP BY backend
		 ORDER BY requests DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*BackendStat
	for rows.Next() {
		s := &BackendStat{}
		if err := rows.Scan(&s.Backend, &s.Requests, &s.TotalTokens, &s.CostUSD, &s.AvgLatencyMs, &s.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

