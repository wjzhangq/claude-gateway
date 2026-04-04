package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// parseNullableTime converts a nullable string from SQLite into *time.Time.
func parseNullableTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	formats := []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 MST m=+0.000000000",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, *s, time.Local); err == nil {
			return &t
		}
	}
	return nil
}

// --- User CRUD ---

func (d *DB) CreateUser(u *model.User) error {
	now := time.Now()
	res, err := d.Exec(
		`INSERT INTO users (itcode, name, role, status, group_id, daily_quota_tokens, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Itcode, u.Name, u.Role, u.Status, u.GroupID, u.DailyQuotaTokens, now, now,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	u.ID, _ = res.LastInsertId()
	u.CreatedAt = now
	u.UpdatedAt = now
	return nil
}

func (d *DB) GetUserByItcode(itcode string) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, itcode, name, role, status, group_id, daily_quota_tokens, aws_enabled, created_at, updated_at
		 FROM users WHERE itcode = ?`, itcode,
	).Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaTokens, &u.AWSEnabled, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, itcode, name, role, status, group_id, daily_quota_tokens, aws_enabled, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaTokens, &u.AWSEnabled, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) ListUsers() ([]*model.User, error) {
	rows, err := d.Query(
		`SELECT id, itcode, name, role, status, group_id, daily_quota_tokens, aws_enabled, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaTokens, &u.AWSEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UserWithStats extends User with aggregated usage info.
type UserWithStats struct {
	model.User
	LastUsedAt     *time.Time `json:"last_used_at"`
	Requests       int64      `json:"requests"`
	CostUSD        float64    `json:"cost_usd"`
	OCCostUSD      float64    `json:"oc_cost_usd"`
	BackendCostUSD float64    `json:"backend_cost_usd"`
	AWSCostUSD     float64    `json:"aws_cost_usd"`
}

func (d *DB) ListUsersWithStats(page, pageSize int) ([]*UserWithStats, int, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := d.Query(
		`SELECT u.id, u.itcode, u.name, u.role, u.status, u.group_id, u.daily_quota_tokens, u.aws_enabled, u.created_at, u.updated_at,
		        MAX(l.created_at) as last_used_at,
		        COALESCE(COUNT(l.id), 0) as requests,
		        COALESCE(SUM(l.cost_usd), 0) as cost_usd,
		        COALESCE(SUM(CASE WHEN l.is_openclaw = 1 THEN l.cost_usd ELSE 0 END), 0) as oc_cost_usd,
		        COALESCE(SUM(l.cost_usd), 0) as backend_cost_usd,
		        COALESCE((SELECT SUM(a.cost_usd) FROM aws_usage_logs a WHERE a.user_id = u.id), 0) as aws_cost_usd
		 FROM users u
		 LEFT JOIN usage_logs l ON l.user_id = u.id
		 GROUP BY u.id
		 ORDER BY u.id DESC
		 LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var users []*UserWithStats
	for rows.Next() {
		u := &UserWithStats{}
		var lastUsed *string
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.GroupID, &u.DailyQuotaTokens, &u.AWSEnabled,
			&u.CreatedAt, &u.UpdatedAt, &lastUsed, &u.Requests, &u.CostUSD, &u.OCCostUSD, &u.BackendCostUSD, &u.AWSCostUSD); err != nil {
			return nil, 0, err
		}
		u.LastUsedAt = parseNullableTime(lastUsed)
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// UpdateUserItcode changes the itcode of a user.
func (d *DB) UpdateUserItcode(id int64, itcode string) error {
	_, err := d.Exec(`UPDATE users SET itcode=?, updated_at=? WHERE id=?`, itcode, time.Now(), id)
	return err
}

// APIKeyWithUser extends APIKey with user info for admin listing.
type APIKeyWithUser struct {
	model.APIKey
	UserItcode    string `json:"user_itcode"`
	UserName      string `json:"user_name"`
	UserAWSEnabled bool  `json:"user_aws_enabled"`
}

// ListAllAPIKeys returns all API keys with pagination, sorted by last_used_at desc, created_at desc.
func (d *DB) ListAllAPIKeys(userID int64, page, pageSize int) ([]*APIKeyWithUser, int, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	args := []interface{}{}
	if userID > 0 {
		where += " AND k.user_id = ?"
		args = append(args, userID)
	}

	var total int
	countArgs := append([]interface{}{}, args...)
	if err := d.QueryRow(`SELECT COUNT(*) FROM api_keys k `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, pageSize, offset)
	rows, err := d.Query(
		`SELECT k.id, k.user_id, u.itcode, u.name, COALESCE(u.aws_enabled, 0), k.key, k.name, k.status, k.channel, k.auto_downgrade, k.last_used_at, k.created_at, k.updated_at,
		        COALESCE(s.requests, 0), COALESCE(s.cost_usd, 0)
		 FROM api_keys k
		 LEFT JOIN users u ON u.id = k.user_id
		 LEFT JOIN (SELECT api_key_id, COUNT(*) as requests, SUM(cost_usd) as cost_usd FROM usage_logs GROUP BY api_key_id) s
		   ON s.api_key_id = k.id
		 `+where+`
		 ORDER BY COALESCE(k.last_used_at, '1970-01-01') DESC, k.created_at DESC
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var keys []*APIKeyWithUser
	for rows.Next() {
		k := &APIKeyWithUser{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.UserItcode, &k.UserName, &k.UserAWSEnabled,
			&k.Key, &k.APIKey.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &lastUsed,
			&k.CreatedAt, &k.UpdatedAt, &k.Requests, &k.CostUSD); err != nil {
			return nil, 0, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, total, rows.Err()
}

// RenameAPIKey updates the name of an API key.
func (d *DB) RenameAPIKey(id int64, name string) error {
	_, err := d.Exec(`UPDATE api_keys SET name=?, updated_at=? WHERE id=?`, name, time.Now(), id)
	return err
}

// TransferAPIKey moves an API key to another user identified by itcode.
// If the user doesn't exist, it is created with active status.
// Returns the target user.
func (d *DB) TransferAPIKey(keyID int64, toItcode string) (*model.User, error) {
	user, err := d.GetUserByItcode(toItcode)
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if user == nil {
		user = &model.User{
			Itcode: toItcode,
			Name:   toItcode,
			Role:   "user",
			Status: "active",
		}
		if err := d.CreateUser(user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	} else if user.Status != "active" {
		user.Status = "active"
		if err := d.UpdateUser(user); err != nil {
			return nil, fmt.Errorf("activate user: %w", err)
		}
	}
	_, err = d.Exec(`UPDATE api_keys SET user_id=?, updated_at=? WHERE id=?`, user.ID, time.Now(), keyID)
	if err != nil {
		return nil, fmt.Errorf("transfer key: %w", err)
	}
	return user, nil
}

func (d *DB) UpdateUser(u *model.User) error {
	u.UpdatedAt = time.Now()
	_, err := d.Exec(
		`UPDATE users SET name=?, role=?, status=?, group_id=?, daily_quota_tokens=?, aws_enabled=?, updated_at=? WHERE id=?`,
		u.Name, u.Role, u.Status, u.GroupID, u.DailyQuotaTokens, u.AWSEnabled, u.UpdatedAt, u.ID,
	)
	return err
}

// EnsureAdmin creates the admin user if no admin exists yet.
func (d *DB) EnsureAdmin(itcode string) error {
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	admin := &model.User{
		Itcode: itcode,
		Name:   "Admin",
		Role:   "admin",
		Status: "active",
	}
	return d.CreateUser(admin)
}

// --- APIKey CRUD ---

func (d *DB) CreateAPIKey(k *model.APIKey) error {
	now := time.Now()
	if k.Channel == "" {
		k.Channel = "backend"
	}
	res, err := d.Exec(
		`INSERT INTO api_keys (user_id, key, name, status, channel, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		k.UserID, k.Key, k.Name, k.Status, k.Channel, now, now,
	)
	if err != nil {
		return fmt.Errorf("create api_key: %w", err)
	}
	k.ID, _ = res.LastInsertId()
	k.CreatedAt = now
	k.UpdatedAt = now
	return nil
}

func (d *DB) GetAPIKeyByKey(key string) (*model.APIKey, error) {
	k := &model.APIKey{}
	err := d.QueryRow(
		`SELECT id, user_id, key, name, status, channel, auto_downgrade, created_at, updated_at
		 FROM api_keys WHERE key = ?`, key,
	).Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (d *DB) ListAPIKeysByUser(userID int64) ([]*model.APIKey, error) {
	rows, err := d.Query(
		`SELECT k.id, k.user_id, k.key, k.name, k.status, k.channel, k.auto_downgrade, k.last_used_at, k.created_at, k.updated_at,
		        COALESCE(s.requests, 0) as requests,
		        COALESCE(s.cost_usd, 0) as cost_usd
		 FROM api_keys k
		 LEFT JOIN (
		     SELECT api_key_id, COUNT(*) as requests, SUM(cost_usd) as cost_usd
		     FROM usage_logs GROUP BY api_key_id
		 ) s ON s.api_key_id = k.id
		 WHERE k.user_id = ?
		 ORDER BY k.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &lastUsed, &k.CreatedAt, &k.UpdatedAt, &k.Requests, &k.CostUSD); err != nil {
			return nil, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// ListAPIKeysByUserAndChannel lists keys for a user filtered by channel.
func (d *DB) ListAPIKeysByUserAndChannel(userID int64, channel string) ([]*model.APIKey, error) {
	rows, err := d.Query(
		`SELECT k.id, k.user_id, k.key, k.name, k.status, k.channel, k.auto_downgrade, k.last_used_at, k.created_at, k.updated_at,
		        COALESCE(s.requests, 0) as requests,
		        COALESCE(s.cost_usd, 0) as cost_usd
		 FROM api_keys k
		 LEFT JOIN (
		     SELECT api_key_id, COUNT(*) as requests, SUM(cost_usd) as cost_usd
		     FROM aws_usage_logs GROUP BY api_key_id
		 ) s ON s.api_key_id = k.id
		 WHERE k.user_id = ? AND k.channel = ?
		 ORDER BY k.id DESC`, userID, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &lastUsed, &k.CreatedAt, &k.UpdatedAt, &k.Requests, &k.CostUSD); err != nil {
			return nil, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (d *DB) ListAllActiveAPIKeys() ([]*model.APIKey, error) {
	rows, err := d.Query(
		`SELECT id, user_id, key, name, status, channel, auto_downgrade, last_used_at, created_at, updated_at
		 FROM api_keys WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &lastUsed, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// UpdateAPIKeyChannel switches the channel of an API key.
func (d *DB) UpdateAPIKeyChannel(id int64, channel string) error {
	_, err := d.Exec(
		`UPDATE api_keys SET channel=?, updated_at=? WHERE id=?`,
		channel, time.Now(), id,
	)
	return err
}

// ListAllAPIKeysByChannel returns API keys filtered by channel, with optional userID filter.
func (d *DB) ListAllAPIKeysByChannel(channel string, userID int64, page, pageSize int) ([]*APIKeyWithUser, int, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	where := "WHERE k.channel = ?"
	args := []interface{}{channel}
	if userID > 0 {
		where += " AND k.user_id = ?"
		args = append(args, userID)
	}

	var total int
	countArgs := append([]interface{}{}, args...)
	if err := d.QueryRow(`SELECT COUNT(*) FROM api_keys k `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(args, pageSize, offset)
	rows, err := d.Query(
		`SELECT k.id, k.user_id, u.itcode, u.name, COALESCE(u.aws_enabled, 0), k.key, k.name, k.status, k.channel, k.auto_downgrade, k.last_used_at, k.created_at, k.updated_at,
		        COALESCE(s.requests, 0), COALESCE(s.cost_usd, 0)
		 FROM api_keys k
		 LEFT JOIN users u ON u.id = k.user_id
		 LEFT JOIN (SELECT api_key_id, COUNT(*) as requests, SUM(cost_usd) as cost_usd FROM aws_usage_logs GROUP BY api_key_id) s
		   ON s.api_key_id = k.id
		 `+where+`
		 ORDER BY COALESCE(k.last_used_at, '1970-01-01') DESC, k.created_at DESC
		 LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var keys []*APIKeyWithUser
	for rows.Next() {
		k := &APIKeyWithUser{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.UserItcode, &k.UserName, &k.UserAWSEnabled,
			&k.Key, &k.APIKey.Name, &k.Status, &k.Channel, &k.AutoDowngrade, &lastUsed,
			&k.CreatedAt, &k.UpdatedAt, &k.Requests, &k.CostUSD); err != nil {
			return nil, 0, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, total, rows.Err()
}

func (d *DB) UpdateAPIKeyStatus(id int64, status string) error {
	_, err := d.Exec(
		`UPDATE api_keys SET status=?, updated_at=? WHERE id=?`,
		status, time.Now(), id,
	)
	return err
}

func (d *DB) UpdateAPIKeyAutoDowngrade(id int64, autoDowngrade bool) error {
	_, err := d.Exec(
		`UPDATE api_keys SET auto_downgrade=?, updated_at=? WHERE id=?`,
		autoDowngrade, time.Now(), id,
	)
	return err
}

func (d *DB) DeleteAPIKey(id int64) error {
	_, err := d.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	return err
}
