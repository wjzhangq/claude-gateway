// cmd/fix 对数据库执行一系列修复操作。
//
// 用法：
//
//	./bin/fix [--db <path>] [--all] [--schema] [--itcode] [--last-used] [--purge] [--purge-days N]
//
// 各修复项：
//
//	--schema     修复表结构：补全缺失列，使其与代码定义一致
//	--itcode     修复用户 itcode：去掉 @domain 后缀（如 yanght5@lenovo.com → yanght5）
//	--last-used  根据 usage_logs 更新 api_keys.last_used_at
//	--purge      清理超过 N 天的 usage_logs（默认 30 天）
//	--all        执行以上全部修复
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "data/gateway.db", "数据库路径")
	all := flag.Bool("all", false, "执行全部修复")
	doSchema := flag.Bool("schema", false, "修复表结构（补全缺失列）")
	doItcode := flag.Bool("itcode", false, "修复用户 itcode（去掉 @domain 后缀）")
	doLastUsed := flag.Bool("last-used", false, "根据 usage_logs 更新 api_keys.last_used_at")
	doPurge := flag.Bool("purge", false, "清理超过 N 天的 usage_logs")
	purgeDays := flag.Int("purge-days", 30, "清理多少天前的数据（配合 --purge 使用）")
	flag.Parse()

	if !*all && !*doSchema && !*doItcode && !*doLastUsed && !*doPurge {
		fmt.Fprintln(os.Stderr, "请指定至少一个修复项，或使用 --all 执行全部。")
		fmt.Fprintln(os.Stderr, "")
		flag.Usage()
		os.Exit(1)
	}

	db, err := openDB(*dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	if *all || *doSchema {
		log.Println("==> [1/4] 修复表结构...")
		n, err := fixSchema(db)
		if err != nil {
			log.Fatalf("修复表结构失败: %v", err)
		}
		log.Printf("    补全列数: %d", n)
	}

	if *all || *doItcode {
		log.Println("==> [2/4] 修复用户 itcode（去掉 @domain）...")
		n, err := fixItcode(db)
		if err != nil {
			log.Fatalf("修复 itcode 失败: %v", err)
		}
		log.Printf("    修复用户数: %d", n)
	}

	if *all || *doLastUsed {
		log.Println("==> [3/4] 根据 usage_logs 更新 api_keys.last_used_at...")
		n, err := fixLastUsed(db)
		if err != nil {
			log.Fatalf("更新 last_used_at 失败: %v", err)
		}
		log.Printf("    更新 Key 数: %d", n)
	}

	if *all || *doPurge {
		log.Printf("==> [4/4] 清理 %d 天前的 usage_logs...", *purgeDays)
		n, err := purgeOldLogs(db, *purgeDays)
		if err != nil {
			log.Fatalf("清理数据失败: %v", err)
		}
		log.Printf("    删除记录数: %d", n)
	}

	log.Println("==> 修复完成")
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// fixSchema 补全代码中定义但数据库中缺失的列，并确保 AWS 表存在。
// 同时修复 applications.model 无 DEFAULT 的问题。
// 返回实际补全的列数。
func fixSchema(db *sql.DB) (int, error) {
	type colDef struct {
		table string
		col   string
		ddl   string
	}

	// 与 internal/db/db.go migrate() 保持完全一致，列出所有应存在的列
	cols := []colDef{
		// users
		{"users", "group_id", "ALTER TABLE users ADD COLUMN group_id INTEGER NOT NULL DEFAULT 0"},
		{"users", "daily_quota_tokens", "ALTER TABLE users ADD COLUMN daily_quota_tokens INTEGER NOT NULL DEFAULT 0"},
		{"users", "aws_enabled", "ALTER TABLE users ADD COLUMN aws_enabled INTEGER NOT NULL DEFAULT 0"},
		// api_keys
		{"api_keys", "auto_downgrade", "ALTER TABLE api_keys ADD COLUMN auto_downgrade INTEGER NOT NULL DEFAULT 0"},
		{"api_keys", "last_used_at", "ALTER TABLE api_keys ADD COLUMN last_used_at DATETIME"},
		{"api_keys", "channel", "ALTER TABLE api_keys ADD COLUMN channel TEXT NOT NULL DEFAULT 'backend'"},
		// usage_logs
		{"usage_logs", "backend", "ALTER TABLE usage_logs ADD COLUMN backend TEXT NOT NULL DEFAULT ''"},
		{"usage_logs", "is_openclaw", "ALTER TABLE usage_logs ADD COLUMN is_openclaw INTEGER NOT NULL DEFAULT 0"},
		{"usage_logs", "is_downgraded", "ALTER TABLE usage_logs ADD COLUMN is_downgraded INTEGER NOT NULL DEFAULT 0"},
		{"usage_logs", "ua", "ALTER TABLE usage_logs ADD COLUMN ua TEXT NOT NULL DEFAULT ''"},
	}

	added := 0
	for _, c := range cols {
		exists, err := columnExists(db, c.table, c.col)
		if err != nil {
			return added, fmt.Errorf("检查列 %s.%s: %w", c.table, c.col, err)
		}
		if exists {
			log.Printf("    [跳过] %s.%s 已存在", c.table, c.col)
			continue
		}
		if _, err := db.Exec(c.ddl); err != nil {
			return added, fmt.Errorf("添加列 %s.%s: %w", c.table, c.col, err)
		}
		log.Printf("    [添加] %s.%s", c.table, c.col)
		added++
	}

	// 确保 AWS 相关表存在
	awsTables := []string{
		`CREATE TABLE IF NOT EXISTS aws_usage_logs (
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
		)`,
		`CREATE TABLE IF NOT EXISTS aws_daily_stats (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_user_id    ON aws_usage_logs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_aws_usage_logs_created_at ON aws_usage_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_date      ON aws_daily_stats(date)`,
		`CREATE INDEX IF NOT EXISTS idx_aws_daily_stats_user_id   ON aws_daily_stats(user_id)`,
	}
	for _, ddl := range awsTables {
		if _, err := db.Exec(ddl); err != nil {
			log.Printf("    [警告] 创建 AWS 表/索引失败: %v", err)
		}
	}

	// 修复 applications.model 无 DEFAULT 问题（SQLite 不支持 ALTER COLUMN，需重建表）
	if n, err := fixApplicationsModelDefault(db); err != nil {
		log.Printf("    [警告] 修复 applications.model DEFAULT 失败: %v", err)
	} else if n > 0 {
		log.Printf("    [修复] applications.model 已补全 DEFAULT ''，迁移行数: %d", n)
		added++
	}

	// 确保所有索引存在
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_api_keys_user_id       ON api_keys(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_logs_user_id     ON usage_logs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_logs_created_at  ON usage_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_stats_date       ON daily_stats(date)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_stats_user_id    ON daily_stats(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_applications_user_id   ON applications(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_applications_status    ON applications(status)`,
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			log.Printf("    [警告] 创建索引失败: %v", err)
		}
	}

	return added, nil
}

// fixApplicationsModelDefault 检查 applications.model 是否缺少 DEFAULT ''。
// 若缺少则通过重建表的方式补全（SQLite 不支持 ALTER COLUMN）。
// 返回迁移的行数（0 表示无需修复）。
func fixApplicationsModelDefault(db *sql.DB) (int, error) {
	// 检查 model 列是否有 DEFAULT 值
	rows, err := db.Query("PRAGMA table_info(applications)")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	needFix := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return 0, err
		}
		if name == "model" && !dflt.Valid {
			needFix = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if !needFix {
		log.Printf("    [跳过] applications.model DEFAULT 已正确")
		return 0, nil
	}

	// 重建 applications 表以补全 DEFAULT
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. 统计行数
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM applications").Scan(&count); err != nil {
		return 0, err
	}

	// 2. 创建新表
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS applications_new (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id     INTEGER NOT NULL REFERENCES users(id),
		model       TEXT    NOT NULL DEFAULT '',
		reason      TEXT    NOT NULL DEFAULT '',
		status      TEXT    NOT NULL DEFAULT 'pending',
		reviewer_id INTEGER,
		review_note TEXT    NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return 0, fmt.Errorf("创建 applications_new: %w", err)
	}

	// 3. 复制数据
	_, err = tx.Exec(`INSERT INTO applications_new
		SELECT id, user_id, COALESCE(model,''), reason, status, reviewer_id, review_note, created_at, updated_at
		FROM applications`)
	if err != nil {
		return 0, fmt.Errorf("复制数据: %w", err)
	}

	// 4. 删除旧表、重命名新表
	if _, err := tx.Exec("DROP TABLE applications"); err != nil {
		return 0, fmt.Errorf("删除旧表: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE applications_new RENAME TO applications"); err != nil {
		return 0, fmt.Errorf("重命名新表: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}

func columnExists(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// fixItcode 将 users.itcode 中包含 @ 的值截断到 @ 之前。
// 若截断后与已有用户冲突，则将当前用户的 key、usage_logs、applications 转移给已存在用户，然后删除当前用户。
func fixItcode(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, itcode FROM users WHERE itcode LIKE '%@%'`)
	if err != nil {
		return 0, fmt.Errorf("查询含 @ 的 itcode: %w", err)
	}
	defer rows.Close()

	type entry struct {
		id     int64
		itcode string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.itcode); err != nil {
			return 0, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	fixed := 0
	for _, e := range entries {
		newItcode := e.itcode
		if idx := strings.Index(e.itcode, "@"); idx > 0 {
			newItcode = e.itcode[:idx]
		}
		if newItcode == e.itcode {
			continue
		}

		// 检查新 itcode 是否已被其他用户占用
		var conflictID int64
		err := db.QueryRow(`SELECT id FROM users WHERE itcode = ? AND id != ?`, newItcode, e.id).Scan(&conflictID)
		if err == nil {
			// 冲突：将当前用户的数据合并到已存在用户，然后删除当前用户
			log.Printf("    [合并] id=%d (%s) → 已存在用户 id=%d (%s)，转移数据后删除", e.id, e.itcode, conflictID, newItcode)
			if mergeErr := mergeUser(db, e.id, conflictID); mergeErr != nil {
				log.Printf("    [警告] 合并失败: %v", mergeErr)
				continue
			}
			fixed++
			continue
		}
		if err != sql.ErrNoRows {
			return fixed, fmt.Errorf("检查冲突 %s: %w", newItcode, err)
		}

		// 无冲突，直接改名
		if _, err := db.Exec(`UPDATE users SET itcode=?, updated_at=? WHERE id=?`, newItcode, time.Now(), e.id); err != nil {
			log.Printf("    [警告] 更新 id=%d 失败: %v", e.id, err)
			continue
		}
		log.Printf("    [修复] id=%d: %s → %s", e.id, e.itcode, newItcode)
		fixed++
	}
	return fixed, nil
}

// mergeUser 将 fromUserID 的所有关联数据转移给 toUserID，然后删除 fromUserID。
// 转移范围：api_keys、usage_logs、applications。
func mergeUser(db *sql.DB, fromUserID, toUserID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 转移 api_keys
	res, err := tx.Exec(`UPDATE api_keys SET user_id=? WHERE user_id=?`, toUserID, fromUserID)
	if err != nil {
		return fmt.Errorf("转移 api_keys: %w", err)
	}
	keysMoved, _ := res.RowsAffected()

	// 转移 usage_logs
	res, err = tx.Exec(`UPDATE usage_logs SET user_id=? WHERE user_id=?`, toUserID, fromUserID)
	if err != nil {
		return fmt.Errorf("转移 usage_logs: %w", err)
	}
	logsMoved, _ := res.RowsAffected()

	// 转移 applications
	res, err = tx.Exec(`UPDATE applications SET user_id=? WHERE user_id=?`, toUserID, fromUserID)
	if err != nil {
		return fmt.Errorf("转移 applications: %w", err)
	}
	appsMoved, _ := res.RowsAffected()

	// 删除旧用户
	if _, err := tx.Exec(`DELETE FROM users WHERE id=?`, fromUserID); err != nil {
		return fmt.Errorf("删除旧用户: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Printf("      keys=%d logs=%d apps=%d 已转移，旧用户 id=%d 已删除", keysMoved, logsMoved, appsMoved, fromUserID)
	return nil
}

// fixLastUsed 根据 usage_logs 中每个 api_key_id 的最大 created_at 无条件覆盖 api_keys.last_used_at。
func fixLastUsed(db *sql.DB) (int, error) {
	res, err := db.Exec(`
		UPDATE api_keys
		SET last_used_at = (
			SELECT MAX(created_at) FROM usage_logs WHERE api_key_id = api_keys.id
		)
		WHERE EXISTS (
			SELECT 1 FROM usage_logs WHERE api_key_id = api_keys.id
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("更新 last_used_at: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// purgeOldLogs 删除 usage_logs 中超过 days 天的记录，返回删除行数。
func purgeOldLogs(db *sql.DB, days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	res, err := db.Exec(`DELETE FROM usage_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("删除旧记录: %w", err)
	}
	return res.RowsAffected()
}
