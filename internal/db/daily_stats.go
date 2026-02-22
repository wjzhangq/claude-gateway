package db

import (
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// AggregateDaily rolls up yesterday's usage_logs into daily_stats.
func (d *DB) AggregateDaily() error {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
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
		WHERE DATE(created_at) = ?
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
