package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"

	"github.com/wjzhangq/claude-gateway/config"
)

// DB wraps the sql.DB connection and a separate read-only connection.
type DB struct {
	*sql.DB
	readonlyDB *sql.DB
	driver     string // "sqlite" or "postgres"
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

	// Separate read-only connection for DB Explorer queries — enforced at driver level.
	roDB, err := sql.Open("sqlite", path+"?mode=ro&_journal_mode=WAL")
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open sqlite (readonly): %w", err)
	}
	roDB.SetMaxOpenConns(4)
	roDB.SetMaxIdleConns(2)

	d := &DB{sqlDB, roDB, "sqlite"}
	if err := d.runMigrations(); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

// InitPostgres opens a PostgreSQL connection and runs schema creation.
func InitPostgres(cfg config.PostgresConfig) (*DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode, cfg.Timezone,
	)
	if cfg.SSLMode == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=%s",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.Timezone,
		)
	}
	if cfg.Timezone == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s TimeZone=Asia/Shanghai",
			cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode,
		)
	}

	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	d := &DB{sqlDB, sqlDB, "postgres"}
	if err := d.runPGSchema(); err != nil {
		d.Close()
		return nil, fmt.Errorf("pg schema: %w", err)
	}

	return d, nil
}

// Driver returns the database driver name.
func (d *DB) Driver() string {
	return d.driver
}

// Close closes both database connections.
func (d *DB) Close() error {
	if d.readonlyDB != nil && d.readonlyDB != d.DB {
		d.readonlyDB.Close()
	}
	return d.DB.Close()
}

func (d *DB) runPGSchema() error {
	_, err := d.Exec(pgSchema)
	return err
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS users (
    id                  BIGSERIAL PRIMARY KEY,
    itcode              TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT 'user',
    status              TEXT NOT NULL DEFAULT 'pending',
    group_id            INTEGER NOT NULL DEFAULT 0,
    daily_quota_tokens  INTEGER NOT NULL DEFAULT 0,
    daily_quota_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
    aws_daily_quota_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    aws_enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id               BIGSERIAL PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id),
    key              TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'active',
    last_used_at     TIMESTAMPTZ,
    auto_downgrade   BOOLEAN NOT NULL DEFAULT FALSE,
    channel          TEXT NOT NULL DEFAULT 'backend',
    total_cost_usd   DOUBLE PRECISION NOT NULL DEFAULT 0,
    backend_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    aws_cost_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);

CREATE TABLE IF NOT EXISTS usage_logs (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL,
    group_id           INTEGER NOT NULL DEFAULT 0,
    api_key_id         BIGINT NOT NULL,
    provider           TEXT NOT NULL DEFAULT 'backend',
    model              TEXT NOT NULL,
    backend_name       TEXT NOT NULL DEFAULT '',
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    total_tokens       INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
    status_code        INTEGER NOT NULL DEFAULT 200,
    latency_ms         BIGINT NOT NULL DEFAULT 0,
    is_openclaw        BOOLEAN NOT NULL DEFAULT FALSE,
    is_downgraded      BOOLEAN NOT NULL DEFAULT FALSE,
    ua                 TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_usage_logs_user_id ON usage_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created_at ON usage_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_user_created ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_group_created ON usage_logs(group_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_provider ON usage_logs(provider);
CREATE INDEX IF NOT EXISTS idx_usage_logs_provider_created ON usage_logs(provider, created_at);

CREATE TABLE IF NOT EXISTS daily_stats (
    id                 BIGSERIAL PRIMARY KEY,
    date               TEXT NOT NULL,
    user_id            BIGINT NOT NULL,
    provider           TEXT NOT NULL DEFAULT 'backend',
    model              TEXT NOT NULL,
    requests           INTEGER NOT NULL DEFAULT 0,
    input_tokens       BIGINT NOT NULL DEFAULT 0,
    output_tokens      BIGINT NOT NULL DEFAULT 0,
    total_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
    UNIQUE(date, user_id, provider, model)
);
CREATE INDEX IF NOT EXISTS idx_daily_stats_date ON daily_stats(date);
CREATE INDEX IF NOT EXISTS idx_daily_stats_user_id ON daily_stats(user_id);
CREATE INDEX IF NOT EXISTS idx_daily_stats_user_date ON daily_stats(user_id, date);
CREATE INDEX IF NOT EXISTS idx_daily_stats_provider ON daily_stats(provider);
CREATE INDEX IF NOT EXISTS idx_daily_stats_provider_date ON daily_stats(provider, date);

CREATE TABLE IF NOT EXISTS applications (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id),
    model       TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending',
    reviewer_id BIGINT,
    review_note TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_applications_user_id ON applications(user_id);
CREATE INDEX IF NOT EXISTS idx_applications_status ON applications(status);

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_state (
    table_name     TEXT PRIMARY KEY,
    last_synced_id BIGINT NOT NULL DEFAULT 0,
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{1, schema},
	{2, awsSchema},
	{3, `SELECT 1`},
	{4, `SELECT 1`},
	{5, `SELECT 1`},
	{6, `SELECT 1`},
	{7, `SELECT 1`},
	{8, `SELECT 1`},
	{9, `SELECT 1`},
	{10, `SELECT 1`},
	{11, `SELECT 1`},
	{12, `SELECT 1`},
	{13, `UPDATE users SET daily_quota_usd = daily_quota_tokens WHERE daily_quota_usd = 0 AND daily_quota_tokens != 0`},
	{14, `SELECT 1`},
	{15, `SELECT 1`},
	{16, `SELECT 1`},
	{17, `SELECT 1`},
	{18, `UPDATE api_keys SET backend_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM usage_logs WHERE api_key_id = api_keys.id), 0) WHERE backend_cost_usd = 0`},
	{19, `UPDATE api_keys SET aws_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM aws_usage_logs WHERE api_key_id = api_keys.id), 0) WHERE aws_cost_usd = 0`},
	{20, `UPDATE api_keys SET total_cost_usd = backend_cost_usd + aws_cost_usd WHERE total_cost_usd = 0`},
	{21, `SELECT 1`},
	{22, `UPDATE usage_logs SET group_id = (SELECT group_id FROM users WHERE id = usage_logs.user_id) WHERE group_id = 0`},
	{23, `SELECT 1`},
	{24, `UPDATE aws_usage_logs SET group_id = (SELECT group_id FROM users WHERE id = aws_usage_logs.user_id) WHERE group_id = 0`},
	{25, `CREATE INDEX IF NOT EXISTS idx_usage_logs_user_created ON usage_logs(user_id, created_at)`},
	{26, `CREATE INDEX IF NOT EXISTS idx_usage_logs_group_created ON usage_logs(group_id, created_at)`},
	{27, `CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_user_created ON aws_usage_logs(user_id, created_at)`},
	{28, `CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_group_created ON aws_usage_logs(group_id, created_at)`},
	{29, `CREATE INDEX IF NOT EXISTS idx_daily_stats_user_date ON daily_stats(user_id, date)`},
	{30, `CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_user_date ON aws_daily_stats(user_id, date)`},
}

func (d *DB) runMigrations() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, m := range migrations {
		var exists int
		if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}
		if _, err := d.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := d.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    itcode              TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL DEFAULT '',
    role                TEXT    NOT NULL DEFAULT 'user',
    status              TEXT    NOT NULL DEFAULT 'pending',
    group_id            INTEGER NOT NULL DEFAULT 0,
    daily_quota_tokens  INTEGER NOT NULL DEFAULT 0,
    daily_quota_usd     REAL    NOT NULL DEFAULT 0,
    aws_daily_quota_usd REAL    NOT NULL DEFAULT 0,
    aws_enabled         INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id),
    key              TEXT    NOT NULL UNIQUE,
    name             TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'active',
    last_used_at     DATETIME,
    auto_downgrade   INTEGER NOT NULL DEFAULT 0,
    channel          TEXT    NOT NULL DEFAULT 'backend',
    total_cost_usd   REAL    NOT NULL DEFAULT 0,
    backend_cost_usd REAL    NOT NULL DEFAULT 0,
    aws_cost_usd     REAL    NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
    is_downgraded INTEGER NOT NULL DEFAULT 0,
    ua            TEXT    NOT NULL DEFAULT '',
    group_id      INTEGER NOT NULL DEFAULT 0,
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
    group_id          INTEGER NOT NULL DEFAULT 0,
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
