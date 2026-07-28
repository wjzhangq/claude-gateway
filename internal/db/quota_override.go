package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

func computeIsExpired(o *model.UserQuotaOverride) {
	if !o.IsTemporary || o.ExpiresAt == nil {
		return
	}
	today := time.Now().Format("2006-01-02")
	o.IsExpired = *o.ExpiresAt < today
}

func scanOverride(row interface {
	Scan(...any) error
}, o *model.UserQuotaOverride) error {
	return row.Scan(
		&o.ID, &o.UserID, &o.Itcode, &o.Name,
		&o.QuotaUSD, &o.IsTemporary, &o.ExpiresAt, &o.Note,
		&o.CreatedAt, &o.UpdatedAt,
	)
}

const overrideSelectSQL = `
SELECT q.id, q.user_id, u.itcode, u.name,
       q.quota_usd, q.is_temporary, q.expires_at, q.note,
       q.created_at, q.updated_at
FROM user_quota_overrides q
JOIN users u ON u.id = q.user_id`

func (d *DB) GetUserQuotaOverride(userID int64) (*model.UserQuotaOverride, error) {
	o := &model.UserQuotaOverride{}
	err := scanOverride(d.QueryRow(overrideSelectSQL+` WHERE q.user_id = ?`, userID), o)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get quota override: %w", err)
	}
	computeIsExpired(o)
	return o, nil
}

func (d *DB) GetUserQuotaOverrideByItcode(itcode string) (*model.UserQuotaOverride, error) {
	o := &model.UserQuotaOverride{}
	err := scanOverride(d.QueryRow(overrideSelectSQL+` WHERE u.itcode = ?`, itcode), o)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get quota override by itcode: %w", err)
	}
	computeIsExpired(o)
	return o, nil
}

func (d *DB) UpsertUserQuotaOverride(userID int64, quotaUSD float64, isTemporary bool, expiresAt *string, note string) error {
	isTmp := 0
	if isTemporary {
		isTmp = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.Exec(`
INSERT INTO user_quota_overrides (user_id, quota_usd, is_temporary, expires_at, note, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id) DO UPDATE SET
  quota_usd    = excluded.quota_usd,
  is_temporary = excluded.is_temporary,
  expires_at   = excluded.expires_at,
  note         = excluded.note,
  updated_at   = excluded.updated_at`,
		userID, quotaUSD, isTmp, expiresAt, note, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert quota override: %w", err)
	}
	return nil
}

func (d *DB) DeleteUserQuotaOverride(userID int64) error {
	_, err := d.Exec(`DELETE FROM user_quota_overrides WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete quota override: %w", err)
	}
	return nil
}

func (d *DB) ListUserQuotaOverrides() ([]*model.UserQuotaOverride, error) {
	rows, err := d.Query(overrideSelectSQL + ` ORDER BY u.itcode ASC`)
	if err != nil {
		return nil, fmt.Errorf("list quota overrides: %w", err)
	}
	defer rows.Close()
	var result []*model.UserQuotaOverride
	for rows.Next() {
		o := &model.UserQuotaOverride{}
		if err := scanOverride(rows, o); err != nil {
			return nil, fmt.Errorf("scan quota override: %w", err)
		}
		computeIsExpired(o)
		result = append(result, o)
	}
	return result, rows.Err()
}

func (d *DB) IsQuotaMigrationDone() (bool, error) {
	var count int
	err := d.QueryRow(`SELECT COUNT(*) FROM quota_migration_flags WHERE key='config_yaml_user_daily_limits'`).Scan(&count)
	return count > 0, err
}

func (d *DB) MarkQuotaMigrationDone() error {
	_, err := d.Exec(`INSERT OR IGNORE INTO quota_migration_flags(key) VALUES('config_yaml_user_daily_limits')`)
	return err
}
