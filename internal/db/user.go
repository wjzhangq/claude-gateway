package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/claude-gateway/claude-gateway/internal/model"
)

// --- User CRUD ---

func (d *DB) CreateUser(u *model.User) error {
	now := time.Now()
	res, err := d.Exec(
		`INSERT INTO users (phone, name, role, status, quota_tokens, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Phone, u.Name, u.Role, u.Status, u.QuotaTokens, now, now,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	u.ID, _ = res.LastInsertId()
	u.CreatedAt = now
	u.UpdatedAt = now
	return nil
}

func (d *DB) GetUserByPhone(phone string) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, phone, name, role, status, quota_tokens, created_at, updated_at
		 FROM users WHERE phone = ?`, phone,
	).Scan(&u.ID, &u.Phone, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, phone, name, role, status, quota_tokens, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Phone, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) ListUsers() ([]*model.User, error) {
	rows, err := d.Query(
		`SELECT id, phone, name, role, status, quota_tokens, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Phone, &u.Name, &u.Role, &u.Status, &u.QuotaTokens, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
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
func (d *DB) EnsureAdmin(phone string) error {
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	admin := &model.User{
		Phone:  phone,
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
		`SELECT id, user_id, key, name, status, expires_at, created_at, updated_at
		 FROM api_keys WHERE user_id = ? ORDER BY id`, userID)
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
