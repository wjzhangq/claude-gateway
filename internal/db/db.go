package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection.
type DB struct {
	*sql.DB
}

// Init opens (or creates) the SQLite database at path and runs migrations.
func Init(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB.SetMaxOpenConns(1) // SQLite write serialization
	sqlDB.SetMaxIdleConns(1)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	d := &DB{sqlDB}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.Exec(schema)
	if err != nil {
		return err
	}

	// Add group_id column if it doesn't exist (for existing databases)
	_, err = d.Exec(`ALTER TABLE users ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		// Ignore error if column already exists
		return err
	}

	// Add is_openclaw column if it doesn't exist (for existing databases)
	_, err = d.Exec(`ALTER TABLE usage_logs ADD COLUMN is_openclaw INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add daily_quota_tokens column if it doesn't exist (rename from quota_tokens)
	_, err = d.Exec(`ALTER TABLE users ADD COLUMN daily_quota_tokens INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add ua column if it doesn't exist (for existing databases)
	_, err = d.Exec(`ALTER TABLE usage_logs ADD COLUMN ua TEXT NOT NULL DEFAULT ''`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add auto_downgrade column to api_keys if it doesn't exist
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN auto_downgrade INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add is_downgraded column to usage_logs if it doesn't exist
	_, err = d.Exec(`ALTER TABLE usage_logs ADD COLUMN is_downgraded INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add last_used_at column to api_keys if it doesn't exist
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN last_used_at DATETIME`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add aws_enabled column to users if it doesn't exist
	_, err = d.Exec(`ALTER TABLE users ADD COLUMN aws_enabled INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add channel column to api_keys if it doesn't exist
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN channel TEXT NOT NULL DEFAULT 'backend'`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Create AWS usage tables
	_, err = d.Exec(awsSchema)
	if err != nil {
		return fmt.Errorf("aws schema: %w", err)
	}

	// Add daily_quota_usd column to users (replaces daily_quota_tokens semantics)
	_, err = d.Exec(`ALTER TABLE users ADD COLUMN daily_quota_usd REAL NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Migrate existing data: copy daily_quota_tokens -> daily_quota_usd (treated as USD values)
	_, _ = d.Exec(`UPDATE users SET daily_quota_usd = daily_quota_tokens WHERE daily_quota_usd = 0 AND daily_quota_tokens != 0`)

	// Add aws_daily_quota_usd column to users
	_, err = d.Exec(`ALTER TABLE users ADD COLUMN aws_daily_quota_usd REAL NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	// Add cost tracking columns to api_keys (write-back pattern like last_used_at)
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN total_cost_usd REAL NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN backend_cost_usd REAL NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	_, err = d.Exec(`ALTER TABLE api_keys ADD COLUMN aws_cost_usd REAL NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Back-fill cost data from usage logs (one-time migration)
	_, _ = d.Exec(`UPDATE api_keys SET backend_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM usage_logs WHERE api_key_id = api_keys.id), 0) WHERE backend_cost_usd = 0`)
	_, _ = d.Exec(`UPDATE api_keys SET aws_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM aws_usage_logs WHERE api_key_id = api_keys.id), 0) WHERE aws_cost_usd = 0`)
	_, _ = d.Exec(`UPDATE api_keys SET total_cost_usd = backend_cost_usd + aws_cost_usd WHERE total_cost_usd = 0`)

	// Add group_id to usage_logs for stable historical group stats
	_, err = d.Exec(`ALTER TABLE usage_logs ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Back-fill group_id from users table
	_, _ = d.Exec(`UPDATE usage_logs SET group_id = (SELECT group_id FROM users WHERE id = usage_logs.user_id) WHERE group_id = 0`)

	// Add group_id to aws_usage_logs
	_, err = d.Exec(`ALTER TABLE aws_usage_logs ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	_, _ = d.Exec(`UPDATE aws_usage_logs SET group_id = (SELECT group_id FROM users WHERE id = aws_usage_logs.user_id) WHERE group_id = 0`)

	// Additional performance indexes
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_logs_user_created ON usage_logs(user_id, created_at)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_logs_group_created ON usage_logs(group_id, created_at)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_user_created ON aws_usage_logs(user_id, created_at)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_group_created ON aws_usage_logs(group_id, created_at)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_stats_user_date ON daily_stats(user_id, date)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_user_date ON aws_daily_stats(user_id, date)`)

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    itcode       TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL DEFAULT '',
    role         TEXT    NOT NULL DEFAULT 'user',
    status       TEXT    NOT NULL DEFAULT 'pending',
    group_id     INTEGER NOT NULL DEFAULT 0,
    daily_quota_tokens INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    key          TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'active',
    last_used_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);

CREATE TABLE IF NOT EXISTS usage_logs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL,
    api_key_id    INTEGER NOT NULL,
    model         TEXT    NOT NULL,
    backend       TEXT    NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL    NOT NULL DEFAULT 0,
    status_code   INTEGER NOT NULL DEFAULT 200,
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    is_openclaw   INTEGER NOT NULL DEFAULT 0,
    ua            TEXT    NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_usage_logs_user_id    ON usage_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created_at ON usage_logs(created_at);

CREATE TABLE IF NOT EXISTS daily_stats (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    date          TEXT    NOT NULL,
    user_id       INTEGER NOT NULL,
    model         TEXT    NOT NULL,
    requests      INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL    NOT NULL DEFAULT 0,
    UNIQUE(date, user_id, model)
);
CREATE INDEX IF NOT EXISTS idx_daily_stats_date    ON daily_stats(date);
CREATE INDEX IF NOT EXISTS idx_daily_stats_user_id ON daily_stats(user_id);

CREATE TABLE IF NOT EXISTS applications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    model       TEXT    NOT NULL DEFAULT '',
    reason      TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'pending',
    reviewer_id INTEGER,
    review_note TEXT    NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_applications_user_id ON applications(user_id);
CREATE INDEX IF NOT EXISTS idx_applications_status  ON applications(status);
`

const awsSchema = `
CREATE TABLE IF NOT EXISTS aws_usage_logs (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           INTEGER NOT NULL,
    api_key_id        INTEGER NOT NULL,
    model             TEXT    NOT NULL,
    bedrock_model     TEXT    NOT NULL DEFAULT '',
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL    NOT NULL DEFAULT 0,
    status_code       INTEGER NOT NULL DEFAULT 200,
    latency_ms        INTEGER NOT NULL DEFAULT 0,
    ua                TEXT    NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_user_id    ON aws_usage_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_created_at ON aws_usage_logs(created_at);

CREATE TABLE IF NOT EXISTS aws_daily_stats (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    date              TEXT    NOT NULL,
    user_id           INTEGER NOT NULL,
    model             TEXT    NOT NULL,
    requests          INTEGER NOT NULL DEFAULT 0,
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL    NOT NULL DEFAULT 0,
    UNIQUE(date, user_id, model)
);
CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_date    ON aws_daily_stats(date);
CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_user_id ON aws_daily_stats(user_id);
`
