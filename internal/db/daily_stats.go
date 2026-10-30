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
	// Use range query to leverage created_at index instead of SUBSTR which causes full table scan.
	// Upper bound is the start of the next day, exclusive: created_at carries
	// nanosecond precision, so an inclusive '23:59:59.999' bound drops rows in
	// the last millisecond of the day by string comparison.
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return fmt.Errorf("aggregate daily stats: bad date %q: %w", date, err)
	}
	startTime := date + " 00:00:00"
	endTime := day.AddDate(0, 0, 1).Format("2006-01-02") + " 00:00:00"
	_, err = d.Exec(`
		INSERT INTO daily_stats (date, user_id, model, requests, input_tokens, output_tokens, total_tokens, cost_usd, oc_cost_usd, last_used_at)
		SELECT
			? as date,
			user_id,
			model,
			COUNT(*) as requests,
			SUM(input_tokens) as input_tokens,
			SUM(output_tokens) as output_tokens,
			SUM(total_tokens) as total_tokens,
			SUM(cost_usd) as cost_usd,
			SUM(CASE WHEN is_openclaw = 1 THEN cost_usd ELSE 0 END) as oc_cost_usd,
			MAX(created_at) as last_used_at
		FROM usage_logs
		WHERE created_at >= ? AND created_at < ?
		GROUP BY user_id, model
		ON CONFLICT(date, user_id, model) DO UPDATE SET
			requests      = excluded.requests,
			input_tokens  = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			total_tokens  = excluded.total_tokens,
			cost_usd      = excluded.cost_usd,
			oc_cost_usd   = excluded.oc_cost_usd,
			last_used_at  = excluded.last_used_at
	`, date, startTime, endTime)
	if err != nil {
		return fmt.Errorf("aggregate daily stats for %s: %w", date, err)
	}
	return nil
}

// maxCatchUpDays caps AggregateSinceLastRollup so a stale or empty daily_stats
// cannot turn startup into a months-long re-aggregation.
const maxCatchUpDays = 90

// AggregateSinceLastRollup re-aggregates every date from the most recent rollup
// through today. AggregateDaily only ever touches today and yesterday, so a
// restart that skips a day leaves that date un-aggregated permanently; this runs
// once at startup to close the gap. Each date uses the created_at index, so the
// cost is proportional to the downtime, not to the table size.
func (d *DB) AggregateSinceLastRollup() error {
	var last string
	if err := d.QueryRowRead(
		`SELECT COALESCE(MAX(date), '') FROM daily_stats WHERE model <> 'synced'`,
	).Scan(&last); err != nil {
		return fmt.Errorf("find last rollup date: %w", err)
	}
	if last == "" {
		// Nothing rolled up yet — migration 56 already rebuilt from scratch.
		return nil
	}

	from, err := time.ParseInLocation("2006-01-02", last, time.Local)
	if err != nil {
		return fmt.Errorf("parse last rollup date %q: %w", last, err)
	}
	// Re-run the last recorded date too: it may have been aggregated mid-day.
	today := time.Now().Truncate(24 * time.Hour)
	if cutoff := time.Now().AddDate(0, 0, -maxCatchUpDays); from.Before(cutoff) {
		from = cutoff
	}

	for day := from; !day.After(today); day = day.AddDate(0, 0, 1) {
		if err := d.aggregateForDate(day.Format("2006-01-02")); err != nil {
			return err
		}
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

	rows, err := d.QueryRead(
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
	startTime := date + " 00:00:00"
	endTime := date + " 23:59:59.999"
	rows, err := d.QueryRead(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM usage_logs WHERE created_at >= ? AND created_at <= ? GROUP BY user_id`,
		startTime, endTime,
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
	startTime := month + "-01 00:00:00"
	// Parse month to calculate the first day of next month
	t, _ := time.Parse("2006-01", month)
	nextMonth := t.AddDate(0, 1, 0).Format("2006-01")
	endTime := nextMonth + "-01 00:00:00"
	rows, err := d.QueryRead(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM aws_usage_logs WHERE created_at >= ? AND created_at < ? GROUP BY user_id`,
		startTime, endTime,
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
	startTime := date + " 00:00:00"
	endTime := date + " 23:59:59.999"
	rows, err := d.QueryRead(
		`SELECT user_id, COALESCE(SUM(cost_usd), 0) FROM aws_usage_logs WHERE created_at >= ? AND created_at <= ? GROUP BY user_id`,
		startTime, endTime,
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
