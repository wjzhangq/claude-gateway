package db

import (
	"fmt"
	"time"
)

// --- Org tag mutations ---

// UpdateUserOrgTag sets the organization tag fields for a single user.
func (d *DB) UpdateUserOrgTag(userID int64, department, roleTag, note string) error {
	if roleTag == "" {
		roleTag = "未分类"
	}
	_, err := d.Exec(
		`UPDATE users SET department=?, role_tag=?, org_note=?, updated_at=? WHERE id=?`,
		department, roleTag, note, time.Now(), userID,
	)
	return err
}

// OrgTagUpdate is one entry in a batch org tag update.
type OrgTagUpdate struct {
	UserID     int64  `json:"user_id"`
	Department string `json:"department"`
	RoleTag    string `json:"role_tag"`
	Note       string `json:"note"`
}

// BatchUpdateOrgTags applies multiple org tag updates in a single transaction.
func (d *DB) BatchUpdateOrgTags(updates []OrgTagUpdate) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE users SET department=?, role_tag=?, org_note=?, updated_at=? WHERE id=?`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now()
	n := 0
	for _, u := range updates {
		roleTag := u.RoleTag
		if roleTag == "" {
			roleTag = "未分类"
		}
		if _, err := stmt.Exec(u.Department, roleTag, u.Note, now, u.UserID); err != nil {
			return 0, fmt.Errorf("update user %d: %w", u.UserID, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// --- Org list ---

// OrgUser is a user row with organization tag fields, for the org management page.
type OrgUser struct {
	UserID     int64  `json:"user_id"`
	Itcode     string `json:"itcode"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Department string `json:"department"`
	RoleTag    string `json:"role_tag"`
	Note       string `json:"note"`
}

// ListOrgUsers returns all users with their org tags, ordered by itcode.
func (d *DB) ListOrgUsers() ([]*OrgUser, error) {
	rows, err := d.Query(
		`SELECT id, itcode, COALESCE(name, ''), status,
		        COALESCE(department, ''), COALESCE(role_tag, '未分类'), COALESCE(org_note, '')
		 FROM users ORDER BY itcode`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OrgUser
	for rows.Next() {
		u := &OrgUser{}
		if err := rows.Scan(&u.UserID, &u.Itcode, &u.Name, &u.Status,
			&u.Department, &u.RoleTag, &u.Note); err != nil {
			return nil, err
		}
		if u.Name == "" {
			u.Name = u.Itcode
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- Usage ranking (backend + aws merged) ---

// RankingUser is one row of the token/cost usage ranking.
type RankingUser struct {
	UserID        int64    `json:"user_id"`
	Itcode        string   `json:"itcode"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Channels      []string `json:"channels"`
	BackendTokens int64    `json:"backend_tokens"`
	BackendInput  int64    `json:"backend_input"`
	BackendOutput int64    `json:"backend_output"`
	AWSTokens     int64    `json:"aws_tokens"`
	AWSInput      int64    `json:"aws_input"`
	AWSOutput     int64    `json:"aws_output"`
	AllTokens     int64    `json:"all_tokens"`
	BackendCost   float64  `json:"backend_cost"`
	AWSCost       float64  `json:"aws_cost"`
	TotalCost     float64  `json:"total_cost"`
	TotalRequests int64    `json:"total_requests"`
	Department    string   `json:"department"`
	RoleTag       string   `json:"role_tag"`
}

// GetUsageRanking returns users ranked by total tokens across backend and aws channels.
// days=0 means all-time; otherwise the last N days (with +8h offset to match CST日界).
func (d *DB) GetUsageRanking(days, limit int) ([]*RankingUser, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	dateFilter := ""
	if days > 0 {
		dateFilter = fmt.Sprintf("WHERE date >= date('now', '+8 hours', '-%d days')", days)
	}
	sql := fmt.Sprintf(`
	SELECT u.id, u.itcode, COALESCE(u.name, ''), u.status,
	  COALESCE(b.total_tokens, 0) as backend_tokens,
	  COALESCE(b.input_tokens, 0) as backend_input,
	  COALESCE(b.output_tokens, 0) as backend_output,
	  COALESCE(a.total_tokens, 0) as aws_tokens,
	  COALESCE(a.input_tokens, 0) as aws_input,
	  COALESCE(a.output_tokens, 0) as aws_output,
	  COALESCE(b.total_tokens, 0) + COALESCE(a.total_tokens, 0) as all_tokens,
	  ROUND(COALESCE(b.cost, 0), 4) as backend_cost,
	  ROUND(COALESCE(a.cost, 0), 4) as aws_cost,
	  ROUND(COALESCE(b.cost, 0) + COALESCE(a.cost, 0), 4) as total_cost,
	  COALESCE(b.requests, 0) + COALESCE(a.requests, 0) as total_requests,
	  COALESCE(u.department, ''), COALESCE(u.role_tag, '未分类')
	FROM users u
	LEFT JOIN (
	  SELECT user_id, SUM(total_tokens) as total_tokens,
	         SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens,
	         SUM(cost_usd) as cost, SUM(requests) as requests
	  FROM daily_stats %s GROUP BY user_id
	) b ON u.id = b.user_id
	LEFT JOIN (
	  SELECT user_id, SUM(total_tokens) as total_tokens,
	         SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens,
	         SUM(cost_usd) as cost, SUM(requests) as requests
	  FROM aws_daily_stats %s GROUP BY user_id
	) a ON u.id = a.user_id
	WHERE COALESCE(b.total_tokens, 0) + COALESCE(a.total_tokens, 0) > 0
	ORDER BY all_tokens DESC
	LIMIT %d`, dateFilter, dateFilter, limit)

	rows, err := d.Query(sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RankingUser
	for rows.Next() {
		r := &RankingUser{}
		if err := rows.Scan(&r.UserID, &r.Itcode, &r.Name, &r.Status,
			&r.BackendTokens, &r.BackendInput, &r.BackendOutput,
			&r.AWSTokens, &r.AWSInput, &r.AWSOutput, &r.AllTokens,
			&r.BackendCost, &r.AWSCost, &r.TotalCost, &r.TotalRequests,
			&r.Department, &r.RoleTag); err != nil {
			return nil, err
		}
		if r.Name == "" {
			r.Name = r.Itcode
		}
		r.Channels = []string{}
		if r.BackendTokens > 0 {
			r.Channels = append(r.Channels, "backend")
		}
		if r.AWSTokens > 0 {
			r.Channels = append(r.Channels, "aws")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RankingMeta holds freshness/totals metadata for the ranking response.
type RankingMeta struct {
	DataUpdatedAt   string `json:"data_updated_at"`
	RegisteredTotal int64  `json:"registered_total"`
}

// GetRankingMeta returns the latest data date and total registered user count.
func (d *DB) GetRankingMeta() (*RankingMeta, error) {
	m := &RankingMeta{}
	var updatedAt *string
	err := d.QueryRow(
		`SELECT MAX(d) FROM (
		   SELECT MAX(date) as d FROM daily_stats
		   UNION ALL SELECT MAX(date) as d FROM aws_daily_stats
		 )`).Scan(&updatedAt)
	if err != nil {
		return nil, err
	}
	if updatedAt != nil {
		m.DataUpdatedAt = *updatedAt
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&m.RegisteredTotal); err != nil {
		return nil, err
	}
	return m, nil
}

// --- Single user insight ---

// ModelStat is per-model aggregation for a user.
type ModelStat struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// TrendPoint is a per-date aggregation for a user.
type TrendPoint struct {
	Date     string  `json:"date"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// WeekPoint is a per-week aggregation.
type WeekPoint struct {
	Week     string  `json:"week"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// PeakDay is a high-usage day for a user.
type PeakDay struct {
	Date   string  `json:"date"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// InsightSummary is the lifetime aggregation for a user on one channel.
type InsightSummary struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Cost         float64 `json:"cost"`
	FirstDate    string  `json:"first_date"`
	LastDate     string  `json:"last_date"`
	ActiveDays   int64   `json:"active_days"`
}

// GetModelDistribution returns per-model stats for a user on the given stats table.
func (d *DB) GetModelDistribution(table string, userID int64) ([]*ModelStat, error) {
	rows, err := d.Query(
		`SELECT model, SUM(requests) as reqs, SUM(total_tokens) as tokens, ROUND(SUM(cost_usd), 4) as cost
		 FROM `+table+` WHERE user_id = ?
		 GROUP BY model ORDER BY cost DESC LIMIT 20`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ModelStat
	for rows.Next() {
		s := &ModelStat{}
		if err := rows.Scan(&s.Model, &s.Requests, &s.Tokens, &s.Cost); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetDailyTrend returns the last 30 days of per-date stats for a user.
func (d *DB) GetDailyTrend(table string, userID int64) ([]*TrendPoint, error) {
	rows, err := d.Query(
		`SELECT date, SUM(requests) as reqs, SUM(total_tokens) as tokens, ROUND(SUM(cost_usd), 4) as cost
		 FROM `+table+` WHERE user_id = ? AND date >= date('now', '+8 hours', '-30 days')
		 GROUP BY date ORDER BY date`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TrendPoint
	for rows.Next() {
		p := &TrendPoint{}
		if err := rows.Scan(&p.Date, &p.Requests, &p.Tokens, &p.Cost); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetWeeklyTrend returns up to 12 weeks of backend stats for a user.
func (d *DB) GetWeeklyTrend(userID int64) ([]*WeekPoint, error) {
	rows, err := d.Query(
		`SELECT strftime('%Y-W%W', date) as week, SUM(requests) as reqs,
		        SUM(total_tokens) as tokens, ROUND(SUM(cost_usd), 4) as cost
		 FROM daily_stats WHERE user_id = ?
		 GROUP BY week ORDER BY week DESC LIMIT 12`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WeekPoint
	for rows.Next() {
		w := &WeekPoint{}
		if err := rows.Scan(&w.Week, &w.Requests, &w.Tokens, &w.Cost); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetPeakDays returns the top 5 highest-token days of backend usage for a user.
func (d *DB) GetPeakDays(userID int64) ([]*PeakDay, error) {
	rows, err := d.Query(
		`SELECT date, SUM(total_tokens) as tokens, ROUND(SUM(cost_usd), 4) as cost
		 FROM daily_stats WHERE user_id = ?
		 GROUP BY date ORDER BY tokens DESC LIMIT 5`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PeakDay
	for rows.Next() {
		p := &PeakDay{}
		if err := rows.Scan(&p.Date, &p.Tokens, &p.Cost); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetInsightSummary returns the lifetime aggregation for a user on the given table.
// Returns nil if the user has no rows (all-NULL aggregate).
func (d *DB) GetInsightSummary(table string, userID int64) (*InsightSummary, error) {
	var reqs, tokens, inputT, outputT, activeDays *int64
	var cost *float64
	var firstDate, lastDate *string
	err := d.QueryRow(
		`SELECT SUM(requests), SUM(total_tokens), SUM(input_tokens), SUM(output_tokens),
		        ROUND(SUM(cost_usd), 4), MIN(date), MAX(date), COUNT(DISTINCT date)
		 FROM `+table+` WHERE user_id = ?`, userID,
	).Scan(&reqs, &tokens, &inputT, &outputT, &cost, &firstDate, &lastDate, &activeDays)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		return nil, nil // no data
	}
	s := &InsightSummary{}
	if reqs != nil {
		s.Requests = *reqs
	}
	s.Tokens = *tokens
	if inputT != nil {
		s.InputTokens = *inputT
	}
	if outputT != nil {
		s.OutputTokens = *outputT
	}
	if cost != nil {
		s.Cost = *cost
	}
	if firstDate != nil {
		s.FirstDate = *firstDate
	}
	if lastDate != nil {
		s.LastDate = *lastDate
	}
	if activeDays != nil {
		s.ActiveDays = *activeDays
	}
	return s, nil
}
