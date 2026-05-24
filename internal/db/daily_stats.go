package db

import (
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// AggregateDaily rolls up today's and yesterday's usage_logs into daily_stats.
func (d *DB) AggregateDaily() error {
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if err := d.aggregateForDate(today); err != nil {
		return err
	}
	return d.aggregateForDate(yesterday)
}

func (d *DB) aggregateForDate(date string) error {
	if d.isPostgres() {
		return d.aggregateForDatePG(date)
	}
	return d.aggregateForDateSQLite(date)
}

func (d *DB) aggregateForDatePG(date string) error {
	_, err := d.Exec(`
		INSERT INTO daily_stats (date, user_id, provider, model, requests, input_tokens, output_tokens, total_tokens,
		    cache_read_tokens, cache_write_tokens, cost_usd)
		SELECT
			$1 as date,
			user_id, provider, model,
			COUNT(*) as requests,
			SUM(input_tokens) as input_tokens,
			SUM(output_tokens) as output_tokens,
			SUM(total_tokens) as total_tokens,
			SUM(cache_read_tokens) as cache_read_tokens,
			SUM(cache_write_tokens) as cache_write_tokens,
			SUM(cost_usd) as cost_usd
		FROM usage_logs
		WHERE TO_CHAR(created_at, 'YYYY-MM-DD') = $2
		GROUP BY user_id, provider, model
		ON CONFLICT(date, user_id, provider, model) DO UPDATE SET
			requests          = excluded.requests,
			input_tokens      = excluded.input_tokens,
			output_tokens     = excluded.output_tokens,
			total_tokens      = excluded.total_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_write_tokens= excluded.cache_write_tokens,
			cost_usd          = excluded.cost_usd
	`, date, date)
	if err != nil {
		return fmt.Errorf("aggregate daily stats (pg) for %s: %w", date, err)
	}
	return nil
}

func (d *DB) aggregateForDateSQLite(date string) error {
	// Aggregate backend usage_logs
	_, err := d.Exec(`
		INSERT INTO daily_stats (date, user_id, model, requests, input_tokens, output_tokens, total_tokens, cost_usd)
		SELECT
			? as date,
			user_id,
			model,
			COUNT(*) as requests,
			SUM(input_tokens) as input_tokens,
			SUM(output_tokens) as output_tokens,
			SUM(total_tokens) as total_tokens,
			SUM(cost_usd) as cost_usd
		FROM usage_logs
		WHERE SUBSTR(created_at, 1, 10) = ?
		GROUP BY user_id, model
		ON CONFLICT(date, user_id, model) DO UPDATE SET
			requests      = excluded.requests,
			input_tokens  = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			total_tokens  = excluded.total_tokens,
			cost_usd      = excluded.cost_usd
	`, date, date)
	if err != nil {
		return fmt.Errorf("aggregate daily stats for %s: %w", date, err)
	}

	// Also aggregate aws_usage_logs
	_, err = d.Exec(`
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

// GetDailyStats queries aggregated stats with optional filters.
func (d *DB) GetDailyStats(userID int64, startDate, endDate, modelFilter string) ([]*model.DailyStats, error) {
	if d.isPostgres() {
		return d.getDailyStatsPG(userID, startDate, endDate, modelFilter, "")
	}

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
		`SELECT id, date, user_id, model, requests, input_tokens, output_tokens, total_tokens, cost_usd
		 FROM daily_stats `+where+` ORDER BY date DESC, user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.DailyStats
	for rows.Next() {
		s := &model.DailyStats{}
		if err := rows.Scan(&s.ID, &s.Date, &s.UserID, &s.Model, &s.Requests,
			&s.InputTokens, &s.OutputTokens, &s.TotalTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		s.Provider = "backend"
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetDailyStatsByProvider queries aggregated stats for a specific provider (PG only).
func (d *DB) GetDailyStatsByProvider(userID int64, startDate, endDate, modelFilter, provider string) ([]*model.DailyStats, error) {
	if d.isPostgres() {
		return d.getDailyStatsPG(userID, startDate, endDate, modelFilter, provider)
	}
	// SQLite: for AWS, read from aws_daily_stats
	if provider == "aws" {
		return d.getAWSDailyStatsSQLite(userID, startDate, endDate, modelFilter)
	}
	return d.GetDailyStats(userID, startDate, endDate, modelFilter)
}

func (d *DB) getDailyStatsPG(userID int64, startDate, endDate, modelFilter, provider string) ([]*model.DailyStats, error) {
	var conditions []string
	var args []interface{}
	argN := 1

	if userID > 0 {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", argN))
		args = append(args, userID)
		argN++
	}
	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("date >= $%d", argN))
		args = append(args, startDate)
		argN++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("date <= $%d", argN))
		args = append(args, endDate)
		argN++
	}
	if modelFilter != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argN))
		args = append(args, modelFilter)
		argN++
	}
	if provider != "" {
		conditions = append(conditions, fmt.Sprintf("provider = $%d", argN))
		args = append(args, provider)
		argN++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + joinConditions(conditions)
	}

	rows, err := d.Query(
		fmt.Sprintf(`SELECT id, date, user_id, provider, model, requests, input_tokens, output_tokens, total_tokens,
		        cache_read_tokens, cache_write_tokens, cost_usd
		 FROM daily_stats %s ORDER BY date DESC, user_id`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.DailyStats
	for rows.Next() {
		s := &model.DailyStats{}
		if err := rows.Scan(&s.ID, &s.Date, &s.UserID, &s.Provider, &s.Model, &s.Requests,
			&s.InputTokens, &s.OutputTokens, &s.TotalTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func joinConditions(conditions []string) string {
	result := ""
	for i, c := range conditions {
		if i > 0 {
			result += " AND "
		}
		result += c
	}
	return result
}

// GetAWSDailyStats queries aws_daily_stats (SQLite backward compat).
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

func (d *DB) getAWSDailyStatsSQLite(userID int64, startDate, endDate, modelFilter string) ([]*model.DailyStats, error) {
	awsStats, err := d.GetAWSDailyStats(userID, startDate, endDate, modelFilter)
	if err != nil {
		return nil, err
	}
	var result []*model.DailyStats
	for _, s := range awsStats {
		result = append(result, &model.DailyStats{
			ID:               s.ID,
			Date:             s.Date,
			UserID:           s.UserID,
			Provider:         "aws",
			Model:            s.Model,
			Requests:         s.Requests,
			InputTokens:      s.InputTokens,
			OutputTokens:     s.OutputTokens,
			TotalTokens:      s.TotalTokens,
			CacheReadTokens:  s.CacheReadTokens,
			CacheWriteTokens: s.CacheWriteTokens,
			CostUSD:          s.CostUSD,
		})
	}
	return result, nil
}

// GetUserDailyCostByDate returns a map of userID -> total backend cost_usd for a given date.
func (d *DB) GetUserDailyCostByDate(date string) (map[int64]float64, error) {
	var query string
	var args []interface{}
	if d.isPostgres() {
		query = `SELECT user_id, COALESCE(SUM(cost_usd), 0)
		         FROM usage_logs
		         WHERE TO_CHAR(created_at, 'YYYY-MM-DD') = $1 AND provider IN ('backend', 'kimi', 'minimax')
		         GROUP BY user_id`
		args = []interface{}{date}
	} else {
		query = `SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM usage_logs WHERE SUBSTR(created_at, 1, 10) = ? GROUP BY user_id`
		args = []interface{}{date}
	}

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get user daily cost for %s: %w", date, err)
	}
	defer rows.Close()

	result := make(map[int64]float64)
	for rows.Next() {
		var userID int64
		var cost float64
		if err := rows.Scan(&userID, &cost); err != nil {
			return nil, err
		}
		result[userID] = cost
	}
	return result, rows.Err()
}

// GetUserAWSDailyCostByDate returns a map of userID -> total AWS cost_usd for a given date.
func (d *DB) GetUserAWSDailyCostByDate(date string) (map[int64]float64, error) {
	var query string
	var args []interface{}
	if d.isPostgres() {
		query = `SELECT user_id, COALESCE(SUM(cost_usd), 0)
		         FROM usage_logs
		         WHERE TO_CHAR(created_at, 'YYYY-MM-DD') = $1 AND provider = 'aws'
		         GROUP BY user_id`
		args = []interface{}{date}
	} else {
		query = `SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM aws_usage_logs WHERE SUBSTR(created_at, 1, 10) = ? GROUP BY user_id`
		args = []interface{}{date}
	}

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get user aws daily cost for %s: %w", date, err)
	}
	defer rows.Close()

	result := make(map[int64]float64)
	for rows.Next() {
		var userID int64
		var cost float64
		if err := rows.Scan(&userID, &cost); err != nil {
			return nil, err
		}
		result[userID] = cost
	}
	return result, rows.Err()
}
