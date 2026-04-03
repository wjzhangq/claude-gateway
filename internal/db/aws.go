package db

import (
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// ===== AWS Usage Logs =====

// InsertAWSUsageLog writes a single AWS usage record.
func (d *DB) InsertAWSUsageLog(log *model.AWSUsageLog) error {
	_, err := d.Exec(
		`INSERT INTO aws_usage_logs
		 (user_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens,
		  cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.UserID, log.APIKeyID, log.Model, log.BedrockModel,
		log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.CacheReadTokens, log.CacheWriteTokens,
		log.CostUSD, log.StatusCode, log.Latency, log.UA,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("insert aws usage log: %w", err)
	}
	return nil
}

// BatchInsertAWSUsageLogs writes multiple AWS usage records in a single transaction.
func (d *DB) BatchInsertAWSUsageLogs(logs []*model.AWSUsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO aws_usage_logs
		 (user_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens,
		  cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	now := time.Now()
	for _, log := range logs {
		ts := now
		if !log.CreatedAt.IsZero() {
			ts = log.CreatedAt
		}
		if _, err := stmt.Exec(
			log.UserID, log.APIKeyID, log.Model, log.BedrockModel,
			log.InputTokens, log.OutputTokens, log.TotalTokens,
			log.CacheReadTokens, log.CacheWriteTokens,
			log.CostUSD, log.StatusCode, log.Latency, log.UA, ts,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec insert: %w", err)
		}
	}
	return tx.Commit()
}

// ListAWSUsageLogs queries aws_usage_logs with optional filters.
func (d *DB) ListAWSUsageLogs(userID int64, startDate, endDate, modelFilter string, page, pageSize int) ([]*model.AWSUsageLog, int, error) {
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
	if err := d.QueryRow("SELECT COUNT(*) FROM aws_usage_logs "+countWhere, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	joinArgs := append(args, pageSize, offset)

	rows, err := d.Query(
		`SELECT l.id, l.user_id, COALESCE(u.itcode,''), l.api_key_id, l.model, l.bedrock_model,
		        l.input_tokens, l.output_tokens, l.total_tokens,
		        l.cache_read_tokens, l.cache_write_tokens,
		        l.cost_usd, l.status_code, l.latency_ms, l.ua, l.created_at
		 FROM aws_usage_logs l LEFT JOIN users u ON u.id = l.user_id
		 `+joinWhere+` ORDER BY l.created_at DESC LIMIT ? OFFSET ?`, joinArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*model.AWSUsageLog
	for rows.Next() {
		l := &model.AWSUsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID,
			&l.Model, &l.BedrockModel,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens,
			&l.CacheReadTokens, &l.CacheWriteTokens,
			&l.CostUSD, &l.StatusCode, &l.Latency, &l.UA, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

// ===== AWS Daily Stats =====

// AggregateAWSDaily rolls up today's and yesterday's aws_usage_logs into aws_daily_stats.
func (d *DB) AggregateAWSDaily() error {
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := d.aggregateAWSForDate(today); err != nil {
		return err
	}
	return d.aggregateAWSForDate(yesterday)
}

func (d *DB) aggregateAWSForDate(date string) error {
	_, err := d.Exec(`
		INSERT INTO aws_daily_stats
		    (date, user_id, model, requests, input_tokens, output_tokens, total_tokens,
		     cache_read_tokens, cache_write_tokens, cost_usd)
		SELECT
		    ? as date,
		    user_id, model,
		    COUNT(*) as requests,
		    SUM(input_tokens) as input_tokens,
		    SUM(output_tokens) as output_tokens,
		    SUM(total_tokens) as total_tokens,
		    SUM(cache_read_tokens) as cache_read_tokens,
		    SUM(cache_write_tokens) as cache_write_tokens,
		    SUM(cost_usd) as cost_usd
		FROM aws_usage_logs
		WHERE SUBSTR(created_at, 1, 10) = ?
		GROUP BY user_id, model
		ON CONFLICT(date, user_id, model) DO UPDATE SET
		    requests          = excluded.requests,
		    input_tokens      = excluded.input_tokens,
		    output_tokens     = excluded.output_tokens,
		    total_tokens      = excluded.total_tokens,
		    cache_read_tokens = excluded.cache_read_tokens,
		    cache_write_tokens= excluded.cache_write_tokens,
		    cost_usd          = excluded.cost_usd
	`, date, date)
	if err != nil {
		return fmt.Errorf("aggregate aws daily stats for %s: %w", date, err)
	}
	return nil
}

// GetAWSDailyStats queries aws_daily_stats with optional filters.
func (d *DB) GetAWSDailyStats(userID int64, startDate, endDate, modelFilter string) ([]*model.AWSDailyStats, error) {
	where := "WHERE 1=1"
	args := []interface{}{}

	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}
	if startDate != "" {
		where += " AND date >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		where += " AND date <= ?"
		args = append(args, endDate)
	}
	if modelFilter != "" {
		where += " AND model = ?"
		args = append(args, modelFilter)
	}

	rows, err := d.Query(
		`SELECT id, date, user_id, model, requests, input_tokens, output_tokens, total_tokens,
		        cache_read_tokens, cache_write_tokens, cost_usd
		 FROM aws_daily_stats `+where+` ORDER BY date DESC, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.AWSDailyStats
	for rows.Next() {
		s := &model.AWSDailyStats{}
		if err := rows.Scan(&s.ID, &s.Date, &s.UserID, &s.Model, &s.Requests,
			&s.InputTokens, &s.OutputTokens, &s.TotalTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// AWSUserDailyCost holds per-user cost for a single day (AWS).
type AWSUserDailyCost struct {
	UserID      int64   `json:"user_id"`
	Itcode      string  `json:"itcode"`
	Requests    int     `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// AWSUserDailyCostResult wraps the ranking list with totals.
type AWSUserDailyCostResult struct {
	Users     []*AWSUserDailyCost `json:"users"`
	TotalCost float64             `json:"total_cost"`
	TotalReqs int                 `json:"total_requests"`
}

// GetAWSUserDailyCostRanking returns top N users by AWS cost for a given date.
func (d *DB) GetAWSUserDailyCostRanking(date string, limit int) (*AWSUserDailyCostResult, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := d.Query(
		`SELECT l.user_id, COALESCE(u.itcode,''), COUNT(*) as requests,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd
		 FROM aws_usage_logs l
		 LEFT JOIN users u ON u.id = l.user_id
		 WHERE SUBSTR(l.created_at, 1, 10) = ?
		 GROUP BY l.user_id
		 ORDER BY cost_usd DESC
		 LIMIT ?`, date, limit)
	if err != nil {
		return nil, fmt.Errorf("get aws user daily cost ranking: %w", err)
	}
	defer rows.Close()

	var users []*AWSUserDailyCost
	for rows.Next() {
		u := &AWSUserDailyCost{}
		if err := rows.Scan(&u.UserID, &u.Itcode, &u.Requests, &u.TotalTokens, &u.CostUSD); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var totalCost float64
	var totalReqs int
	err = d.QueryRow(
		`SELECT COALESCE(SUM(cost_usd),0), COUNT(*)
		 FROM aws_usage_logs WHERE SUBSTR(created_at, 1, 10) = ?`, date).Scan(&totalCost, &totalReqs)
	if err != nil {
		return nil, fmt.Errorf("get aws daily totals: %w", err)
	}

	return &AWSUserDailyCostResult{
		Users:     users,
		TotalCost: totalCost,
		TotalReqs: totalReqs,
	}, nil
}

// BedrockStat holds aggregated usage for a single Bedrock model.
type BedrockStat struct {
	BedrockModel string  `json:"bedrock_model"`
	Requests     int     `json:"requests"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	ErrorCount   int     `json:"error_count"`
}

// GetAWSBedrockStats aggregates aws_usage_logs by bedrock_model for the given date range.
func (d *DB) GetAWSBedrockStats(startDate, endDate string) ([]*BedrockStat, error) {
	where := "WHERE 1=1"
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
		`SELECT bedrock_model,
		        COUNT(*) as requests,
		        SUM(total_tokens) as total_tokens,
		        SUM(cost_usd) as cost_usd,
		        AVG(latency_ms) as avg_latency_ms,
		        SUM(CASE WHEN status_code != 200 THEN 1 ELSE 0 END) as error_count
		 FROM aws_usage_logs `+where+`
		 GROUP BY bedrock_model
		 ORDER BY requests DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*BedrockStat
	for rows.Next() {
		s := &BedrockStat{}
		if err := rows.Scan(&s.BedrockModel, &s.Requests, &s.TotalTokens, &s.CostUSD, &s.AvgLatencyMs, &s.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// ListAWSUsers returns users with aws_enabled=true and their AWS usage summary.
type AWSUserWithStats struct {
	model.User
	AWSRequests int64   `json:"aws_requests"`
	AWSCostUSD  float64 `json:"aws_cost_usd"`
}

// ListAWSUsersWithStats returns all aws_enabled users with their total AWS usage.
func (d *DB) ListAWSUsersWithStats(page, pageSize int) ([]*AWSUserWithStats, int, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE aws_enabled = 1`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := d.Query(
		`SELECT u.id, u.itcode, u.name, u.role, u.status, u.group_id, u.daily_quota_tokens, u.aws_enabled, u.created_at, u.updated_at,
		        COALESCE(s.requests,0), COALESCE(s.cost_usd,0)
		 FROM users u
		 LEFT JOIN (SELECT user_id, COUNT(*) as requests, SUM(cost_usd) as cost_usd FROM aws_usage_logs GROUP BY user_id) s
		   ON s.user_id = u.id
		 WHERE u.aws_enabled = 1
		 ORDER BY u.id DESC
		 LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []*AWSUserWithStats
	for rows.Next() {
		u := &AWSUserWithStats{}
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaTokens, &u.AWSEnabled,
			&u.CreatedAt, &u.UpdatedAt, &u.AWSRequests, &u.AWSCostUSD); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}
