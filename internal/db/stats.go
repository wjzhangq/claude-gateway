package db

import (
	"fmt"
	"strings"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// InsertUsageLog writes a single usage record to the database.
func (d *DB) InsertUsageLog(log *model.UsageLog) error {
	if d.isPostgres() {
		_, err := d.Exec(
			`INSERT INTO usage_logs
			 (user_id, group_id, api_key_id, provider, model, backend_name, input_tokens, output_tokens, total_tokens,
			  cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			log.UserID, log.GroupID, log.APIKeyID, log.Provider, log.Model, log.BackendName,
			log.InputTokens, log.OutputTokens, log.TotalTokens,
			log.CacheReadTokens, log.CacheWriteTokens,
			log.CostUSD, log.StatusCode, log.Latency,
			log.IsOpenClaw, log.IsDowngraded, log.UA, time.Now(),
		)
		return err
	}
	_, err := d.Exec(
		`INSERT INTO usage_logs
		 (user_id, group_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.UserID, log.GroupID, log.APIKeyID, log.Model, log.BackendName,
		log.InputTokens, log.OutputTokens, log.TotalTokens,
		log.CostUSD, log.StatusCode, log.Latency,
		log.IsOpenClaw, log.IsDowngraded, log.UA, time.Now(),
	)
	return err
}

// BatchInsertUsageLogs writes multiple usage records in a single transaction.
func (d *DB) BatchInsertUsageLogs(logs []*model.UsageLog) error {
	if len(logs) == 0 {
		return nil
	}

	if d.isPostgres() {
		return d.batchInsertUsageLogsPG(logs)
	}
	return d.batchInsertUsageLogsSQLite(logs)
}

func (d *DB) batchInsertUsageLogsPG(logs []*model.UsageLog) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO usage_logs
		 (user_id, group_id, api_key_id, provider, model, backend_name, input_tokens, output_tokens, total_tokens,
		  cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`)
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
			log.UserID, log.GroupID, log.APIKeyID, log.Provider, log.Model, log.BackendName,
			log.InputTokens, log.OutputTokens, log.TotalTokens,
			log.CacheReadTokens, log.CacheWriteTokens,
			log.CostUSD, log.StatusCode, log.Latency,
			log.IsOpenClaw, log.IsDowngraded, log.UA, ts,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec insert: %w", err)
		}
	}
	return tx.Commit()
}

func (d *DB) batchInsertUsageLogsSQLite(logs []*model.UsageLog) error {
	// Split: AWS records go to aws_usage_logs, others go to usage_logs
	var backendLogs []*model.UsageLog
	var awsLogs []*model.AWSUsageLog
	for _, log := range logs {
		if log.Provider == "aws" {
			awsLogs = append(awsLogs, &model.AWSUsageLog{
				UserID:           log.UserID,
				GroupID:          log.GroupID,
				APIKeyID:         log.APIKeyID,
				Model:            log.Model,
				BedrockModel:     log.BackendName,
				InputTokens:      log.InputTokens,
				OutputTokens:     log.OutputTokens,
				TotalTokens:      log.TotalTokens,
				CacheReadTokens:  log.CacheReadTokens,
				CacheWriteTokens: log.CacheWriteTokens,
				CostUSD:          log.CostUSD,
				StatusCode:       log.StatusCode,
				Latency:          log.Latency,
				UA:               log.UA,
				CreatedAt:        log.CreatedAt,
			})
		} else {
			backendLogs = append(backendLogs, log)
		}
	}

	if len(backendLogs) > 0 {
		if err := d.batchInsertBackendLogsSQLite(backendLogs); err != nil {
			return err
		}
	}
	if len(awsLogs) > 0 {
		if err := d.BatchInsertAWSUsageLogs(awsLogs); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) batchInsertBackendLogsSQLite(logs []*model.UsageLog) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO usage_logs
		 (user_id, group_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens, cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			log.UserID, log.GroupID, log.APIKeyID, log.Model, log.BackendName,
			log.InputTokens, log.OutputTokens, log.TotalTokens,
			log.CostUSD, log.StatusCode, log.Latency,
			log.IsOpenClaw, log.IsDowngraded, log.UA, ts,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec insert: %w", err)
		}
	}
	return tx.Commit()
}

// BatchInsertAWSUsageLogs writes multiple AWS usage records (SQLite only, for backward compat).
func (d *DB) BatchInsertAWSUsageLogs(logs []*model.AWSUsageLog) error {
	if len(logs) == 0 {
		return nil
	}
	// On PG, AWS records go through the unified usage_logs table via BatchInsertUsageLogs.
	// This method exists only for SQLite backward compatibility.
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO aws_usage_logs
		 (user_id, group_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens,
		  cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, ua, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			log.UserID, log.GroupID, log.APIKeyID, log.Model, log.BedrockModel,
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

// BatchUpdateKeyLastUsedAt updates last_used_at for multiple keys in one transaction.
func (d *DB) BatchUpdateKeyLastUsedAt(keyTimes map[int64]time.Time) error {
	if len(keyTimes) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	var stmt_sql string
	if d.isPostgres() {
		stmt_sql = `UPDATE api_keys SET last_used_at=$1 WHERE id=$2`
	} else {
		stmt_sql = `UPDATE api_keys SET last_used_at=? WHERE id=?`
	}
	stmt, err := tx.Prepare(stmt_sql)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	for keyID, t := range keyTimes {
		if _, err := stmt.Exec(t, keyID); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec update: %w", err)
		}
	}
	return tx.Commit()
}

// KeyCostUpdate holds pending cost delta for one key.
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
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	var stmt_sql string
	if d.isPostgres() {
		stmt_sql = `UPDATE api_keys
		 SET backend_cost_usd = backend_cost_usd + $1,
		     aws_cost_usd     = aws_cost_usd     + $2,
		     total_cost_usd   = total_cost_usd   + $3
		 WHERE id = $4`
	} else {
		stmt_sql = `UPDATE api_keys
		 SET backend_cost_usd = backend_cost_usd + ?,
		     aws_cost_usd     = aws_cost_usd     + ?,
		     total_cost_usd   = total_cost_usd   + ?
		 WHERE id = ?`
	}
	stmt, err := tx.Prepare(stmt_sql)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()
	for _, u := range updates {
		total := u.BackendCostAdd + u.AWSCostAdd
		if _, err := stmt.Exec(u.BackendCostAdd, u.AWSCostAdd, total, u.KeyID); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec update: %w", err)
		}
	}
	return tx.Commit()
}

// ListUsageLogs queries usage logs with optional filters (PG version uses provider field).
func (d *DB) ListUsageLogs(userID int64, startDate, endDate, modelFilter, providerFilter string, page, pageSize int) ([]*model.UsageLog, int, error) {
	if d.isPostgres() {
		return d.listUsageLogsPG(userID, startDate, endDate, modelFilter, providerFilter, page, pageSize)
	}
	return d.listUsageLogsSQLite(userID, startDate, endDate, modelFilter, providerFilter, page, pageSize)
}

func (d *DB) listUsageLogsPG(userID int64, startDate, endDate, modelFilter, providerFilter string, page, pageSize int) ([]*model.UsageLog, int, error) {
	var conditions []string
	var args []interface{}
	argN := 1

	if userID > 0 {
		conditions = append(conditions, fmt.Sprintf("user_id = $%d", argN))
		args = append(args, userID)
		argN++
	}
	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argN))
		args = append(args, startDate)
		argN++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argN))
		args = append(args, endDate+" 23:59:59")
		argN++
	}
	if modelFilter != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argN))
		args = append(args, modelFilter)
		argN++
	}
	if providerFilter != "" {
		conditions = append(conditions, fmt.Sprintf("provider = $%d", argN))
		args = append(args, providerFilter)
		argN++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM usage_logs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	queryArgs := append(args, pageSize, offset)

	query := fmt.Sprintf(
		`SELECT l.id, l.user_id, COALESCE(u.itcode,''), l.api_key_id, l.provider, l.model, l.backend_name,
		        l.input_tokens, l.output_tokens, l.total_tokens, l.cache_read_tokens, l.cache_write_tokens,
		        l.cost_usd, l.status_code, l.latency_ms, l.is_openclaw, l.is_downgraded, l.ua, l.created_at
		 FROM usage_logs l LEFT JOIN users u ON u.id = l.user_id
		 %s ORDER BY l.created_at DESC LIMIT $%d OFFSET $%d`, where, argN, argN+1)

	rows, err := d.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Provider, &l.Model, &l.BackendName,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CacheReadTokens, &l.CacheWriteTokens,
			&l.CostUSD, &l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

func (d *DB) listUsageLogsSQLite(userID int64, startDate, endDate, modelFilter, backendFilter string, page, pageSize int) ([]*model.UsageLog, int, error) {
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
	if page <= 0 {
		page = 1
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
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Model, &l.BackendName,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CostUSD,
			&l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		// For SQLite, set provider based on backend field
		l.Provider = "backend"
		if strings.HasPrefix(l.BackendName, "public:kimi") {
			l.Provider = "kimi"
		} else if strings.HasPrefix(l.BackendName, "public:minimax") {
			l.Provider = "minimax"
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

// ListUsageLogsAll returns all matching usage logs without pagination (for CSV export).
func (d *DB) ListUsageLogsAll(userID int64, startDate, endDate, modelFilter, backendFilter string) ([]*model.UsageLog, error) {
	if d.isPostgres() {
		return d.listUsageLogsAllPG(userID, startDate, endDate, modelFilter, backendFilter)
	}
	return d.listUsageLogsAllSQLite(userID, startDate, endDate, modelFilter, backendFilter)
}

func (d *DB) listUsageLogsAllPG(userID int64, startDate, endDate, modelFilter, providerFilter string) ([]*model.UsageLog, error) {
	var conditions []string
	var args []interface{}
	argN := 1

	if userID > 0 {
		conditions = append(conditions, fmt.Sprintf("l.user_id = $%d", argN))
		args = append(args, userID)
		argN++
	}
	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("l.created_at >= $%d", argN))
		args = append(args, startDate)
		argN++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("l.created_at <= $%d", argN))
		args = append(args, endDate+" 23:59:59")
		argN++
	}
	if modelFilter != "" {
		conditions = append(conditions, fmt.Sprintf("l.model = $%d", argN))
		args = append(args, modelFilter)
		argN++
	}
	if providerFilter != "" {
		conditions = append(conditions, fmt.Sprintf("l.provider = $%d", argN))
		args = append(args, providerFilter)
		argN++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := d.Query(
		fmt.Sprintf(`SELECT l.id, l.user_id, COALESCE(u.itcode,''), l.api_key_id, l.provider, l.model, l.backend_name,
		        l.input_tokens, l.output_tokens, l.total_tokens, l.cache_read_tokens, l.cache_write_tokens,
		        l.cost_usd, l.status_code, l.latency_ms, l.is_openclaw, l.is_downgraded, l.ua, l.created_at
		 FROM usage_logs l LEFT JOIN users u ON u.id = l.user_id %s ORDER BY l.created_at ASC`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*model.UsageLog
	for rows.Next() {
		l := &model.UsageLog{}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Provider, &l.Model, &l.BackendName,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CacheReadTokens, &l.CacheWriteTokens,
			&l.CostUSD, &l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (d *DB) listUsageLogsAllSQLite(userID int64, startDate, endDate, modelFilter, backendFilter string) ([]*model.UsageLog, error) {
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
		if err := rows.Scan(&l.ID, &l.UserID, &l.Itcode, &l.APIKeyID, &l.Model, &l.BackendName,
			&l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.CostUSD,
			&l.StatusCode, &l.Latency, &l.IsOpenClaw, &l.IsDowngraded, &l.UA, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.Provider = "backend"
		if strings.HasPrefix(l.BackendName, "public:kimi") {
			l.Provider = "kimi"
		} else if strings.HasPrefix(l.BackendName, "public:minimax") {
			l.Provider = "minimax"
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
	Users     []*UserDailyCost `json:"users"`
	TotalCost float64          `json:"total_cost"`
	OCCost    float64          `json:"oc_cost"`
	TotalReqs int              `json:"total_requests"`
}

// GetUserDailyCostRanking returns top N users by cost for a given date, plus totals.
func (d *DB) GetUserDailyCostRanking(date string, limit int) (*UserDailyCostResult, error) {
	if limit <= 0 {
		limit = 20
	}

	if d.isPostgres() {
		return d.getUserDailyCostRankingPG(date, limit)
	}

	rows, err := d.Query(
		`SELECT l.user_id, COALESCE(u.itcode, ''), COUNT(*) as requests,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd,
		        SUM(CASE WHEN l.is_openclaw THEN l.cost_usd ELSE 0 END) as oc_cost_usd
		 FROM usage_logs l
		 LEFT JOIN users u ON u.id = l.user_id
		 WHERE SUBSTR(l.created_at, 1, 10) = ?
		 GROUP BY l.user_id
		 ORDER BY cost_usd DESC
		 LIMIT ?`, date, limit)
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

func (d *DB) getUserDailyCostRankingPG(date string, limit int) (*UserDailyCostResult, error) {
	rows, err := d.Query(
		`SELECT l.user_id, COALESCE(u.itcode, ''), COUNT(*) as requests,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd,
		        SUM(CASE WHEN l.is_openclaw THEN l.cost_usd ELSE 0 END) as oc_cost_usd
		 FROM usage_logs l
		 LEFT JOIN users u ON u.id = l.user_id
		 WHERE TO_CHAR(l.created_at, 'YYYY-MM-DD') = $1
		 GROUP BY l.user_id, u.itcode
		 ORDER BY cost_usd DESC
		 LIMIT $2`, date, limit)
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

	var totalCost, ocCost float64
	var totalReqs int
	err = d.QueryRow(
		`SELECT COALESCE(SUM(cost_usd),0), COALESCE(SUM(CASE WHEN is_openclaw THEN cost_usd ELSE 0 END),0), COUNT(*)
		 FROM usage_logs WHERE TO_CHAR(created_at, 'YYYY-MM-DD') = $1`, date).Scan(&totalCost, &ocCost, &totalReqs)
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
	if d.isPostgres() {
		return d.getBackendStatsPG(startDate, endDate)
	}

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

func (d *DB) getBackendStatsPG(startDate, endDate string) ([]*BackendStat, error) {
	var conditions []string
	var args []interface{}
	argN := 1

	conditions = append(conditions, "backend_name != ''")
	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argN))
		args = append(args, startDate)
		argN++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argN))
		args = append(args, endDate+" 23:59:59")
		argN++
	}

	where := "WHERE " + strings.Join(conditions, " AND ")

	rows, err := d.Query(
		`SELECT backend_name,
		        COUNT(*) as requests,
		        SUM(total_tokens) as total_tokens,
		        SUM(cost_usd) as cost_usd,
		        AVG(latency_ms) as avg_latency_ms,
		        SUM(CASE WHEN status_code != 200 THEN 1 ELSE 0 END) as error_count
		 FROM usage_logs `+where+`
		 GROUP BY backend_name
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

// GetGroupStats aggregates usage_logs by group for the given date range.
func (d *DB) GetGroupStats(startDate, endDate string) ([]*model.GroupStats, error) {
	if d.isPostgres() {
		return d.getGroupStatsPG(startDate, endDate)
	}

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

func (d *DB) getGroupStatsPG(startDate, endDate string) ([]*model.GroupStats, error) {
	var conditions []string
	var args []interface{}
	argN := 1

	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("l.created_at >= $%d", argN))
		args = append(args, startDate)
		argN++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("l.created_at <= $%d", argN))
		args = append(args, endDate+" 23:59:59")
		argN++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := d.Query(
		fmt.Sprintf(`SELECT l.group_id,
		        COUNT(*) as requests,
		        SUM(l.input_tokens) as input_tokens,
		        SUM(l.output_tokens) as output_tokens,
		        SUM(l.total_tokens) as total_tokens,
		        SUM(l.cost_usd) as cost_usd
		 FROM usage_logs l
		 %s
		 GROUP BY l.group_id
		 ORDER BY l.group_id`, where), args...)
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

// ProviderStats holds aggregated usage for a provider with model breakdown.
type ProviderModelStat struct {
	Model        string  `json:"model"`
	Requests     int     `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// GetProviderStats returns aggregate stats for a given provider and period.
func (d *DB) GetProviderStats(provider, period string) (int, float64, error) {
	if !d.isPostgres() {
		return 0, 0, fmt.Errorf("provider stats only available on postgres")
	}

	var dateFilter string
	switch period {
	case "month":
		dateFilter = fmt.Sprintf("TO_CHAR(created_at, 'YYYY-MM') = '%s'", time.Now().Format("2006-01"))
	default: // today
		dateFilter = fmt.Sprintf("TO_CHAR(created_at, 'YYYY-MM-DD') = '%s'", time.Now().Format("2006-01-02"))
	}

	var requests int
	var costUSD float64
	err := d.QueryRow(
		fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(cost_usd), 0) FROM usage_logs WHERE provider = $1 AND %s`, dateFilter),
		provider).Scan(&requests, &costUSD)
	return requests, costUSD, err
}

// GetProviderModelStats returns per-model stats for a given provider and date.
func (d *DB) GetProviderModelStats(provider, date string) ([]*ProviderModelStat, error) {
	if !d.isPostgres() {
		return nil, fmt.Errorf("provider model stats only available on postgres")
	}

	rows, err := d.Query(
		`SELECT model, COUNT(*) as requests, SUM(input_tokens) as input_tokens,
		        SUM(output_tokens) as output_tokens, SUM(total_tokens) as total_tokens,
		        SUM(cost_usd) as cost_usd
		 FROM usage_logs
		 WHERE provider = $1 AND TO_CHAR(created_at, 'YYYY-MM-DD') = $2
		 GROUP BY model
		 ORDER BY cost_usd DESC`, provider, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*ProviderModelStat
	for rows.Next() {
		s := &ProviderModelStat{}
		if err := rows.Scan(&s.Model, &s.Requests, &s.InputTokens, &s.OutputTokens, &s.TotalTokens, &s.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// ListAWSUsageLogs queries aws_usage_logs (SQLite only) with optional filters.
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
	BedrockModel     string  `json:"bedrock_model"`
	Requests         int     `json:"requests"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	ErrorCount       int     `json:"error_count"`
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
		        SUM(input_tokens) as input_tokens,
		        SUM(output_tokens) as output_tokens,
		        SUM(total_tokens) as total_tokens,
		        SUM(cache_read_tokens) as cache_read_tokens,
		        SUM(cache_write_tokens) as cache_write_tokens,
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
		if err := rows.Scan(&s.BedrockModel, &s.Requests,
			&s.InputTokens, &s.OutputTokens, &s.TotalTokens,
			&s.CacheReadTokens, &s.CacheWriteTokens,
			&s.CostUSD, &s.AvgLatencyMs, &s.ErrorCount); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// AWSUserWithStats holds aws_enabled user with stats.
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
		`SELECT u.id, u.itcode, u.name, u.role, u.status, u.group_id, u.daily_quota_usd, u.aws_daily_quota_usd, u.aws_enabled, u.created_at, u.updated_at,
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
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaUSD, &u.AWSDailyQuotaUSD, &u.AWSEnabled,
			&u.CreatedAt, &u.UpdatedAt, &u.AWSRequests, &u.AWSCostUSD); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}
