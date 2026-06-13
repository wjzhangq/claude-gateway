package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the sql.DB connection and a separate read-only connection.
type DB struct {
	*sql.DB
	readonlyDB *sql.DB
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

	d := &DB{sqlDB, roDB}
	if err := d.runMigrations(); err != nil {
		d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

// Close closes both database connections.
func (d *DB) Close() error {
	if d.readonlyDB != nil {
		d.readonlyDB.Close()
	}
	return d.DB.Close()
}

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{1, schema},
	{2, awsSchema},
	{3, `SELECT 1`}, // group_id already in initial schema; was duplicate ALTER
	{4, `SELECT 1`}, // is_openclaw already in initial schema
	{5, `SELECT 1`}, // daily_quota_tokens already in initial schema
	{6, `SELECT 1`}, // ua already in initial schema
	{7, `SELECT 1`},  // auto_downgrade already in initial schema
	{8, `SELECT 1`},  // is_downgraded already in initial schema
	{9, `SELECT 1`},  // last_used_at already in initial schema
	{10, `SELECT 1`}, // aws_enabled already in initial schema
	{11, `SELECT 1`}, // channel already in initial schema
	{12, `SELECT 1`}, // daily_quota_usd already in initial schema
	{13, `UPDATE users SET daily_quota_usd = daily_quota_tokens WHERE daily_quota_usd = 0 AND daily_quota_tokens != 0`},
	{14, `SELECT 1`}, // aws_daily_quota_usd already in initial schema
	{15, `SELECT 1`}, // total_cost_usd already in initial schema
	{16, `SELECT 1`}, // backend_cost_usd already in initial schema
	{17, `SELECT 1`}, // aws_cost_usd already in initial schema
	{18, `UPDATE api_keys SET backend_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM usage_logs WHERE api_key_id = api_keys.id), 0) WHERE backend_cost_usd = 0`},
	{19, `UPDATE api_keys SET aws_cost_usd = COALESCE((SELECT SUM(cost_usd) FROM aws_usage_logs WHERE api_key_id = api_keys.id), 0) WHERE aws_cost_usd = 0`},
	{20, `UPDATE api_keys SET total_cost_usd = backend_cost_usd + aws_cost_usd WHERE total_cost_usd = 0`},
	{21, `SELECT 1`}, // group_id already in initial schema for usage_logs
	{22, `UPDATE usage_logs SET group_id = (SELECT group_id FROM users WHERE id = usage_logs.user_id) WHERE group_id = 0`},
	{23, `SELECT 1`}, // group_id already in initial schema for aws_usage_logs
	{24, `UPDATE aws_usage_logs SET group_id = (SELECT group_id FROM users WHERE id = aws_usage_logs.user_id) WHERE group_id = 0`},
	{25, `CREATE INDEX IF NOT EXISTS idx_usage_logs_user_created ON usage_logs(user_id, created_at)`},
	{26, `CREATE INDEX IF NOT EXISTS idx_usage_logs_group_created ON usage_logs(group_id, created_at)`},
	{27, `CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_user_created ON aws_usage_logs(user_id, created_at)`},
	{28, `CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_group_created ON aws_usage_logs(group_id, created_at)`},
	{29, `CREATE INDEX IF NOT EXISTS idx_daily_stats_user_date ON daily_stats(user_id, date)`},
	{30, `CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_user_date ON aws_daily_stats(user_id, date)`},
	{31, perfTestSchema},
	{32, `ALTER TABLE api_keys ADD COLUMN locked_model TEXT NOT NULL DEFAULT ''`},
}

const perfTestSchema = `
CREATE TABLE IF NOT EXISTS perf_test_runs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    initiated_by    TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'running',
    channels        TEXT NOT NULL,
    input_sizes     TEXT NOT NULL,
    output_sizes    TEXT NOT NULL,
    total_cells     INTEGER NOT NULL,
    completed_cells INTEGER NOT NULL DEFAULT 0,
    error_msg       TEXT
);

CREATE TABLE IF NOT EXISTS perf_test_results (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id              INTEGER NOT NULL REFERENCES perf_test_runs(id),
    channel             TEXT NOT NULL,
    model               TEXT NOT NULL,
    input_tokens        INTEGER NOT NULL,
    max_tokens          INTEGER NOT NULL,
    ttft_ms             REAL,
    tpot_ms             REAL,
    tokens_per_second   REAL,
    actual_output_tokens INTEGER,
    total_duration_ms   REAL,
    status              TEXT NOT NULL DEFAULT 'pending',
    error_msg           TEXT,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_perf_test_results_run_id ON perf_test_results(run_id);
`

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
