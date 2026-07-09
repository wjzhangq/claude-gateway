package db

import (
	"context"
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// InsertUsageLog writes a single usage record to the database.
func (d *DB) InsertUsageLog(log *model.UsageLog) error {
	_, err := d.Exec(
		`INSERT INTO usage_logs
		 (user_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.UserID, log.APIKeyID, log.Model, log.Backend,
		log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.CostUSD, log.StatusCode, log.Latency,
		log.IsOpenClaw, log.IsDowngraded, log.UA,
		time.Now(),
	)
	if err != nil {
		return fmt.Errorf("insert usage log: %w", err)
	}
	return nil
}

// BatchInsertUsageLogs writes multiple usage records in a single transaction.
func (d *DB) BatchInsertUsageLogs(logs []*model.UsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() // 兜底：Commit 成功后为 no-op；任何错误路径都保证清理事务，避免连接残留事务
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO usage_logs
		 (user_id, group_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	now := time.Now()
	for _, log := range logs {
		ts := now
		if !log.CreatedAt.IsZero() {
			ts = log.CreatedAt
		}
		if _, err := stmt.ExecContext(ctx,
			log.UserID, log.GroupID, log.APIKeyID, log.Model, log.Backend,
			log.InputTokens, log.OutputTokens, log.TotalTokens,
			log.CostUSD, log.StatusCode, log.Latency,
			log.IsOpenClaw, log.IsDowngraded, log.UA, ts,
		); err != nil {
			return fmt.Errorf("exec insert: %w", err)
		}
	}
	return tx.Commit()
}

// BatchUpdateKeyLastUsedAt updates last_used_at for multiple keys in one transaction.
// keyTimes maps api_key_id -> last used time.
func (d *DB) BatchUpdateKeyLastUsedAt(keyTimes map[int64]time.Time) error {
	if len(keyTimes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() // 兜底：Commit 成功后为 no-op；任何错误路径都保证清理事务，避免连接残留事务
	stmt, err := tx.PrepareContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	for keyID, t := range keyTimes {
		if _, err := stmt.ExecContext(ctx, t, keyID); err != nil {
			return fmt.Errorf("exec update: %w", err)
		}
	}
	return tx.Commit()
}

// KeyCostUpdate holds pending cost delta for one key (mirrors auth.KeyCostUpdate).
type KeyCostUpdate struct {
	KeyID          int64
	BackendCostAdd float64
	AWSCostAdd     float64
}

// BatchUpdateKeyCosts atomically increments backend/aws/total cost columns on api_keys.
func (d *DB) BatchUpdateKeyCosts(updates []KeyCostUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), txTimeout)
	defer cancel()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() // 兜底：Commit 成功后为 no-op；任何错误路径都保证清理事务，避免连接残留事务
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE api_keys
		 SET backend_cost_usd = backend_cost_usd + ?,
		     aws_cost_usd     = aws_cost_usd     + ?,
		     total_cost_usd   = total_cost_usd   + ?
		 WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	for _, u := range updates {
		total := u.BackendCostAdd + u.AWSCostAdd
		if _, err := stmt.ExecContext(ctx, u.BackendCostAdd, u.AWSCostAdd, total, u.KeyID); err != nil {
			return fmt.Errorf("exec update: %w", err)
		}
	}
	return tx.Commit()
}

// ListUsageLogs queries usage logs with optional filters.
// backendFilter supports prefix matching: e.g. "public:" matches all public providers.
func (d *DB) ListUsageLogs(userID int64, startDate, endDate, modelFilter, backendFilter string, page, pageSize int) ([]*model.UsageLog, int, error) {
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
	if backendFilter != "" {
		countWhere += " AND backend LIKE ?"
		joinWhere += " AND l.backend LIKE ?"
		args = append(args, backendFilter+"%")
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
		`SELECT l.id, l.user_id, u.itcode, l.api_key_id, l.model, l.backend, l.input_tokens, l.output_tokens, l.total_tokens, l.cost_usd, l.status_code, l.latency_ms, l.is_openclaw, l.is_downgraded, l.ua, l.created_at
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
			&l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

// ListUsageLogsAll returns all matching usage logs without pagination (for CSV export).
func (d *DB) ListUsageLogsAll(userID int64, startDate, endDate, modelFilter, backendFilter string) ([]*model.UsageLog, error) {
	where := "WHERE 1=1"
	args := []interface{}{}

	if userID > 0 {
		where += " AND l.user_id = ?"
		args = append(args, userID)
	}
	if startDate != "" {
		where += " AND l.created_at >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		where += " AND l.created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}
	if modelFilter != "" {
		where += " AND l.model = ?"
		args = append(args, modelFilter)
	}
	if backendFilter != "" {
		where += " AND l.backend LIKE ?"
		args = append(args, backendFilter+"%")
	}

	rows, err := d.Query(
		`SELECT l.id, l.user_id, u.itcode, l.api_key_id, l.model, l.backend, l.input_tokens, l.output_tokens, l.total_tokens, l.cost_usd, l.status_code, l.latency_ms, l.is_openclaw, l.is_downgraded, l.ua, l.created_at
		 FROM usage_logs l LEFT JOIN users u ON u.id = l.user_id `+where+` ORDER BY l.created_at ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Model, &l.Backend,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CostUSD,
			&l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// UserDailyCost holds per-user cost for a single day.
type UserDailyCost struct {
	UserID      int64   `json:"user_id"`
	Itcode      string  `json:"itcode"`
	Requests    int     `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	OCCostUSD   float64 `json:"oc_cost_usd"`
}

// UserDailyCostResult wraps the ranking list with totals.
type UserDailyCostResult struct {
	Users      []*UserDailyCost `json:"users"`
	TotalCost  float64          `json:"total_cost"`
	OCCost     float64          `json:"oc_cost"`
	TotalReqs  int              `json:"total_requests"`
}

// GetUserDailyCostRanking returns top N users by cost for a given date, plus totals.
func (d *DB) GetUserDailyCostRanking(date string, limit int, hidden []string) (*UserDailyCostResult, error) {
	if limit <= 0 {
		limit = 20
	}

	hiddenClause, hiddenArgs := hiddenItcodeClause("u.itcode", hidden)

	// Top N users
	listArgs := []interface{}{date}
	listArgs = append(listArgs, hiddenArgs...)
	listArgs = append(listArgs, limit)
	rows, err := d.Query(
		`SELECT l.user_id, COALESCE(u.itcode, ''), COUNT(*) as requests,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd,
		        SUM(CASE WHEN l.is_openclaw THEN l.cost_usd ELSE 0 END) as oc_cost_usd
		 FROM usage_logs l
		 LEFT JOIN users u ON u.id = l.user_id
		 WHERE SUBSTR(l.created_at, 1, 10) = ?`+hiddenClause+`
		 GROUP BY l.user_id
		 ORDER BY cost_usd DESC
		 LIMIT ?`, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("get user daily cost ranking: %w", err)
	}
	defer rows.Close()

	var users []*UserDailyCost
	for rows.Next() {
		u := &UserDailyCost{}
		if err := rows.Scan(&u.UserID, &u.Itcode, &u.Requests, &u.TotalTokens, &u.CostUSD, &u.OCCostUSD); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Totals
	// Totals include hidden itcodes — they are only excluded from the ranked list.
	var totalCost, ocCost float64
	var totalReqs int
	err = d.QueryRow(
		`SELECT COALESCE(SUM(cost_usd),0), COALESCE(SUM(CASE WHEN is_openclaw THEN cost_usd ELSE 0 END),0), COUNT(*)
		 FROM usage_logs WHERE SUBSTR(created_at, 1, 10) = ?`, date).Scan(&totalCost, &ocCost, &totalReqs)
	if err != nil {
		return nil, fmt.Errorf("get daily totals: %w", err)
	}

	return &UserDailyCostResult{
		Users:     users,
		TotalCost: totalCost,
		OCCost:    ocCost,
		TotalReqs: totalReqs,
	}, nil
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

// UserBackendDailyStat holds today's usage for one backend, for a specific user.
type UserBackendDailyStat struct {
	Backend     string  `json:"backend"`
	Requests    int     `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// GetUserTodayBackendBreakdown returns today's usage grouped by backend for the given user.
func (d *DB) GetUserTodayBackendBreakdown(userID int64, date string) ([]*UserBackendDailyStat, error) {
	rows, err := d.Query(
		`SELECT backend, COUNT(*) as requests, SUM(total_tokens) as total_tokens, SUM(cost_usd) as cost_usd
		 FROM usage_logs
		 WHERE user_id = ? AND SUBSTR(created_at, 1, 10) = ? AND backend != ''
		 GROUP BY backend
		 ORDER BY cost_usd DESC`, userID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*UserBackendDailyStat
	for rows.Next() {
		s := &UserBackendDailyStat{}
		if err := rows.Scan(&s.Backend, &s.Requests, &s.TotalTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// GetGroupStats aggregates usage_logs by group for the given date range.
func (d *DB) GetGroupStats(startDate, endDate string) ([]*model.GroupStats, error) {
	where := "WHERE 1=1"
	args := []interface{}{}

	if startDate != "" {
		where += " AND l.created_at >= ?"
		args = append(args, startDate)
	}
	if endDate != "" {
		where += " AND l.created_at <= ?"
		args = append(args, endDate+" 23:59:59")
	}

	rows, err := d.Query(
		`SELECT l.group_id,
		        COUNT(*) as requests,
		        SUM(l.input_tokens) as input_tokens,
		        SUM(l.output_tokens) as output_tokens,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd
		 FROM usage_logs l
		 `+where+`
		 GROUP BY l.group_id
		 ORDER BY l.group_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.GroupStats
	for rows.Next() {
		s := &model.GroupStats{}
		if err := rows.Scan(&s.GroupID, &s.Requests, &s.InputTokens, &s.OutputTokens, &s.TotalTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
