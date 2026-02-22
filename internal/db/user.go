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
		if t, err := time.Parse(f, *s); err == nil {
			return &t
		}
	}
	return nil
}

// --- User CRUD ---

func (d *DB) CreateUser(u *model.User) error {
	now := time.Now()
	res, err := d.Exec(
		`INSERT INTO users (itcode, name, role, status, quota_tokens, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Itcode, u.Name, u.Role, u.Status, u.QuotaTokens, now, now,
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
		`SELECT id, itcode, name, role, status, quota_tokens, created_at, updated_at
		 FROM users WHERE itcode = ?`, itcode,
	).Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, itcode, name, role, status, quota_tokens, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) ListUsers() ([]*model.User, error) {
	rows, err := d.Query(
		`SELECT id, itcode, name, role, status, quota_tokens, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UserWithStats extends User with aggregated usage info.
type UserWithStats struct {
	model.User
	LastUsedAt  *time.Time `json:"last_used_at"`
	TotalTokens int64      `json:"total_tokens"`
}

func (d *DB) ListUsersWithStats() ([]*UserWithStats, error) {
	rows, err := d.Query(
		`SELECT u.id, u.itcode, u.name, u.role, u.status, u.quota_tokens, u.created_at, u.updated_at,
		        MAX(l.created_at) as last_used_at,
		        COALESCE(SUM(l.total_tokens), 0) as total_tokens
		 FROM users u
		 LEFT JOIN usage_logs l ON l.user_id = u.id
		 GROUP BY u.id
		 ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*UserWithStats
	for rows.Next() {
		u := &UserWithStats{}
		var lastUsed *string
		if err := rows.Scan(&u.ID, &u.Itcode, &u.Name, &u.Role, &u.Status, &u.QuotaTokens,
			&u.CreatedAt, &u.UpdatedAt, &lastUsed, &u.TotalTokens); err != nil {
			return nil, err
		}
		u.LastUsedAt = parseNullableTime(lastUsed)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (d *DB) UpdateUser(u *model.User) error {
	u.UpdatedAt = time.Now()
	_, err := d.Exec(
		`UPDATE users SET name=?, role=?, status=?, quota_tokens=?, updated_at=? WHERE id=?`,
		u.Name, u.Role, u.Status, u.QuotaTokens, u.UpdatedAt, u.ID,
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
	res, err := d.Exec(
		`INSERT INTO api_keys (user_id, key, name, status, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		k.UserID, k.Key, k.Name, k.Status, k.ExpiresAt, now, now,
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
		`SELECT id, user_id, key, name, status, expires_at, created_at, updated_at
		 FROM api_keys WHERE key = ?`, key,
	).Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (d *DB) ListAPIKeysByUser(userID int64) ([]*model.APIKey, error) {
	rows, err := d.Query(
		`SELECT k.id, k.user_id, k.key, k.name, k.status, k.expires_at, k.created_at, k.updated_at,
		        MAX(l.created_at) as last_used_at
		 FROM api_keys k
		 LEFT JOIN usage_logs l ON l.api_key_id = k.id
		 WHERE k.user_id = ?
		 GROUP BY k.id
		 ORDER BY k.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		var lastUsed *string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &lastUsed); err != nil {
			return nil, err
		}
		k.LastUsedAt = parseNullableTime(lastUsed)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (d *DB) ListAllActiveAPIKeys() ([]*model.APIKey, error) {
	rows, err := d.Query(
		`SELECT id, user_id, key, name, status, expires_at, created_at, updated_at
		 FROM api_keys WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*model.APIKey
	for rows.Next() {
		k := &model.APIKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.Name, &k.Status, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (d *DB) UpdateAPIKeyStatus(id int64, status string) error {
	_, err := d.Exec(
		`UPDATE api_keys SET status=?, updated_at=? WHERE id=?`,
		status, time.Now(), id,
	)
	return err
}

func (d *DB) DeleteAPIKey(id int64) error {
	_, err := d.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	return err
}
