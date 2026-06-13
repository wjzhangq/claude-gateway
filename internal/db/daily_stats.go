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
	return nil
}

// GetDailyStats queries aggregated stats with optional filters.
func (d *DB) GetDailyStats(userID int64, startDate, endDate, modelFilter string) ([]*model.DailyStats, error) {
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
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetUserDailyCostByDate returns a map of userID -> total backend cost_usd for a given date.
// Used at startup to seed the in-memory daily cost tracking in KeyStore.
// Reads directly from usage_logs (not daily_stats) to avoid the aggregation lag
// that would cause backend_daily_used to drift from the real-time today cost.
func (d *DB) GetUserDailyCostByDate(date string) (map[int64]float64, error) {
	rows, err := d.Query(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM usage_logs WHERE SUBSTR(created_at, 1, 10) = ? GROUP BY user_id`,
		date,
	)
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

// GetUserAWSMonthlyCostByMonth returns a map of userID -> total AWS cost_usd for a given month.
// month must be in "YYYY-MM" format. Used at startup to seed the in-memory monthly cost tracking.
// Reads directly from aws_usage_logs to capture costs not yet aggregated into aws_daily_stats.
func (d *DB) GetUserAWSMonthlyCostByMonth(month string) (map[int64]float64, error) {
	rows, err := d.Query(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM aws_usage_logs WHERE SUBSTR(created_at, 1, 7) = ? GROUP BY user_id`,
		month,
	)
	if err != nil {
		return nil, fmt.Errorf("get user aws monthly cost for %s: %w", month, err)
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
// Used at startup to seed the in-memory AWS daily cost tracking in KeyStore.
// Reads directly from aws_usage_logs (not aws_daily_stats) to avoid the aggregation lag.
func (d *DB) GetUserAWSDailyCostByDate(date string) (map[int64]float64, error) {
	rows, err := d.Query(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM aws_usage_logs WHERE SUBSTR(created_at, 1, 10) = ? GROUP BY user_id`,
		date,
	)
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
