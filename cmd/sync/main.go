// cmd/sync 将 SQLite 数据库 (gateway.db) 增量同步到 PostgreSQL。
// 支持多次运行（幂等），使用 sync_state 表追踪同步进度。
//
// 用法：
//
//	./bin/sync --config ./config/config.yaml
//	./bin/sync --fromdb ./data/gateway.db --config ./config/config.yaml
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/lib/pq"
	_ "modernc.org/sqlite"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/db"
)

const batchSize = 50000

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "配置文件路径")
	fromPath := flag.String("fromdb", "", "SQLite 源数据库路径（默认取配置中的 database.path）")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	sqlitePath := *fromPath
	if sqlitePath == "" {
		sqlitePath = cfg.Database.Path
	}
	if sqlitePath == "" {
		fmt.Fprintln(os.Stderr, "错误: 未指定 SQLite 源数据库路径 (--fromdb 或 config 中 database.path)")
		os.Exit(1)
	}

	if cfg.Database.Postgres.Host == "" {
		fmt.Fprintln(os.Stderr, "错误: 配置中未找到 PostgreSQL 配置")
		os.Exit(1)
	}

	fromDB, err := openSQLite(sqlitePath)
	if err != nil {
		log.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer fromDB.Close()

	// Use db.InitPostgres to ensure schema exists
	pgDB, err := db.InitPostgres(cfg.Database.Postgres)
	if err != nil {
		log.Fatalf("连接 PostgreSQL 失败: %v", err)
	}
	defer pgDB.Close()
	toDB := pgDB.DB // underlying *sql.DB

	startTime := time.Now()
	log.Printf("开始同步: %s → PostgreSQL (%s:%d/%s)",
		sqlitePath, cfg.Database.Postgres.Host, cfg.Database.Postgres.Port, cfg.Database.Postgres.DBName)

	s := &syncer{from: fromDB, to: toDB}

	// Print source counts for progress overview
	log.Println("==> 统计源数据...")
	s.printSourceCounts()

	log.Println("==> 同步用户...")
	inserted, updated, err := s.syncUsers()
	if err != nil {
		log.Fatalf("同步用户失败: %v", err)
	}
	log.Printf("    ✓ 完成: 新增 %d, 更新 %d", inserted, updated)

	log.Println("==> 同步 API Key...")
	inserted, updated, err = s.syncAPIKeys()
	if err != nil {
		log.Fatalf("同步 API Key 失败: %v", err)
	}
	log.Printf("    ✓ 完成: 新增 %d, 更新 %d", inserted, updated)

	log.Println("==> 同步 usage_logs (backend/kimi/minimax)...")
	count, err := s.syncUsageLogs()
	if err != nil {
		log.Fatalf("同步 usage_logs 失败: %v", err)
	}
	log.Printf("    ✓ 完成: %d 条", count)

	log.Println("==> 同步 aws_usage_logs...")
	count, err = s.syncAWSUsageLogs()
	if err != nil {
		log.Fatalf("同步 aws_usage_logs 失败: %v", err)
	}
	log.Printf("    ✓ 完成: %d 条", count)

	log.Println("==> 同步 daily_stats...")
	count, err = s.syncDailyStats()
	if err != nil {
		log.Fatalf("同步 daily_stats 失败: %v", err)
	}
	log.Printf("    ✓ 完成: %d 条", count)

	log.Println("==> 同步 aws_daily_stats...")
	count, err = s.syncAWSDailyStats()
	if err != nil {
		log.Fatalf("同步 aws_daily_stats 失败: %v", err)
	}
	log.Printf("    ✓ 完成: %d 条", count)

	log.Println("==> 同步 applications...")
	count, err = s.syncApplications()
	if err != nil {
		log.Fatalf("同步 applications 失败: %v", err)
	}
	log.Printf("    ✓ 完成: %d 条", count)

	log.Printf("==> 全部同步完成! 耗时 %s", time.Since(startTime).Round(time.Millisecond))
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&mode=ro&_busy_timeout=5000")
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


type syncer struct {
	from *sql.DB
	to   *sql.DB
}

func (s *syncer) printSourceCounts() {
	tables := []struct {
		name  string
		query string
	}{
		{"users", "SELECT COUNT(*) FROM users"},
		{"api_keys", "SELECT COUNT(*) FROM api_keys"},
		{"usage_logs", "SELECT COUNT(*) FROM usage_logs"},
		{"aws_usage_logs", "SELECT COUNT(*) FROM aws_usage_logs"},
		{"daily_stats", "SELECT COUNT(*) FROM daily_stats"},
		{"aws_daily_stats", "SELECT COUNT(*) FROM aws_daily_stats"},
		{"applications", "SELECT COUNT(*) FROM applications"},
	}
	for _, t := range tables {
		var count int64
		if err := s.from.QueryRow(t.query).Scan(&count); err != nil {
			log.Printf("    %s: (查询失败)", t.name)
			continue
		}
		log.Printf("    %s: %d 条", t.name, count)
	}
}

func (s *syncer) countSource(table string) int64 {
	var count int64
	s.from.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
	return count
}

// getLastSyncedID reads sync progress from PG sync_state table.
func (s *syncer) getLastSyncedID(tableName string) (int64, error) {
	var id int64
	err := s.to.QueryRow(`SELECT last_synced_id FROM sync_state WHERE table_name = $1`, tableName).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// updateLastSyncedID updates sync progress in PG sync_state table.
func (s *syncer) updateLastSyncedID(tableName string, lastID int64) error {
	_, err := s.to.Exec(
		`INSERT INTO sync_state (table_name, last_synced_id, last_synced_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (table_name) DO UPDATE SET last_synced_id = $2, last_synced_at = NOW()`,
		tableName, lastID,
	)
	return err
}

// syncUsers UPSERT users from SQLite to PG on itcode.
func (s *syncer) syncUsers() (int, int, error) {
	rows, err := s.from.Query(
		`SELECT id, itcode, name, role, status, group_id, daily_quota_tokens, daily_quota_usd,
		        aws_daily_quota_usd, aws_enabled, created_at, updated_at
		 FROM users ORDER BY id`)
	if err != nil {
		return 0, 0, fmt.Errorf("查询源用户: %w", err)
	}
	defer rows.Close()

	tx, err := s.to.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO users (itcode, name, role, status, group_id, daily_quota_tokens, daily_quota_usd,
		                    aws_daily_quota_usd, aws_enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (itcode) DO UPDATE SET
		   name = EXCLUDED.name, role = EXCLUDED.role, status = EXCLUDED.status,
		   group_id = EXCLUDED.group_id, daily_quota_tokens = EXCLUDED.daily_quota_tokens,
		   daily_quota_usd = EXCLUDED.daily_quota_usd, aws_daily_quota_usd = EXCLUDED.aws_daily_quota_usd,
		   aws_enabled = EXCLUDED.aws_enabled, updated_at = EXCLUDED.updated_at`)
	if err != nil {
		tx.Rollback()
		return 0, 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for rows.Next() {
		var (
			id          int64
			itcode, name string
			role, status string
			groupID      int
			quotaTokens  int
			quotaUSD     float64
			awsQuotaUSD  float64
			awsEnabled   int
			createdAt    string
			updatedAt    string
		)
		if err := rows.Scan(&id, &itcode, &name, &role, &status, &groupID,
			&quotaTokens, &quotaUSD, &awsQuotaUSD, &awsEnabled, &createdAt, &updatedAt); err != nil {
			tx.Rollback()
			return 0, 0, fmt.Errorf("读取用户行: %w", err)
		}
		if _, err := stmt.Exec(itcode, name, role, status, groupID, quotaTokens, quotaUSD,
			awsQuotaUSD, awsEnabled != 0, parseTime(createdAt), parseTime(updatedAt)); err != nil {
			log.Printf("    [警告] UPSERT 用户 %s 失败: %v", itcode, err)
			continue
		}
		inserted++
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return 0, 0, err
	}
	return inserted, 0, tx.Commit()
}

// syncAPIKeys UPSERT api_keys from SQLite to PG on key.
func (s *syncer) syncAPIKeys() (int, int, error) {
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, 0, err
	}
	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, 0, err
	}

	rows, err := s.from.Query(
		`SELECT id, user_id, key, name, status, last_used_at, auto_downgrade, channel,
		        total_cost_usd, backend_cost_usd, aws_cost_usd, created_at, updated_at
		 FROM api_keys ORDER BY id`)
	if err != nil {
		return 0, 0, fmt.Errorf("查询源 API Key: %w", err)
	}
	defer rows.Close()

	tx, err := s.to.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO api_keys (user_id, key, name, status, last_used_at, auto_downgrade, channel,
		                       total_cost_usd, backend_cost_usd, aws_cost_usd, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (key) DO UPDATE SET
		   user_id = EXCLUDED.user_id, name = EXCLUDED.name, status = EXCLUDED.status,
		   last_used_at = EXCLUDED.last_used_at, auto_downgrade = EXCLUDED.auto_downgrade,
		   channel = EXCLUDED.channel, total_cost_usd = EXCLUDED.total_cost_usd,
		   backend_cost_usd = EXCLUDED.backend_cost_usd, aws_cost_usd = EXCLUDED.aws_cost_usd,
		   updated_at = EXCLUDED.updated_at`)
	if err != nil {
		tx.Rollback()
		return 0, 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for rows.Next() {
		var (
			id            int64
			userID        int64
			key, name     string
			status        string
			lastUsedAt    sql.NullString
			autoDowngrade int
			channel       string
			totalCost     float64
			backendCost   float64
			awsCost       float64
			createdAt     string
			updatedAt     string
		)
		if err := rows.Scan(&id, &userID, &key, &name, &status, &lastUsedAt, &autoDowngrade,
			&channel, &totalCost, &backendCost, &awsCost, &createdAt, &updatedAt); err != nil {
			tx.Rollback()
			return 0, 0, fmt.Errorf("读取 API Key 行: %w", err)
		}

		itcode, ok := sqliteUserMap[userID]
		if !ok {
			log.Printf("    [警告] API Key %s... 的 user_id %d 无对应用户，跳过", key[:min(8, len(key))], userID)
			continue
		}
		pgUserID, ok := pgUserMap[itcode]
		if !ok {
			log.Printf("    [警告] API Key 用户 %s 在 PG 中不存在，跳过", itcode)
			continue
		}

		var lastUsed any
		if lastUsedAt.Valid && lastUsedAt.String != "" {
			lastUsed = parseTime(lastUsedAt.String)
		}

		if _, err := stmt.Exec(pgUserID, key, name, status, lastUsed, autoDowngrade != 0, channel,
			totalCost, backendCost, awsCost, parseTime(createdAt), parseTime(updatedAt)); err != nil {
			log.Printf("    [警告] UPSERT API Key 失败: %v", err)
			continue
		}
		inserted++
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return 0, 0, err
	}
	return inserted, 0, tx.Commit()
}

// syncUsageLogs incrementally syncs usage_logs from SQLite to PG.
// Infers provider from the "backend" column:
//   - "public:kimi" prefix → provider='kimi'
//   - "public:minimax" prefix → provider='minimax'
//   - otherwise → provider='backend'
func (s *syncer) syncUsageLogs() (int, error) {
	lastID, err := s.getLastSyncedID("usage_logs")
	if err != nil {
		return 0, fmt.Errorf("获取同步进度: %w", err)
	}

	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, err
	}
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, err
	}
	pgKeyMap, err := s.buildPGKeyMap()
	if err != nil {
		return 0, err
	}

	var pendingCount int64
	s.from.QueryRow(`SELECT COUNT(*) FROM usage_logs WHERE id > ?`, lastID).Scan(&pendingCount)
	if pendingCount > 0 {
		log.Printf("    待同步: %d 条 (从 id=%d 开始)", pendingCount, lastID)
	}

	total := 0
	startTime := time.Now()
	lastLog := time.Now()
	for {
		rows, err := s.from.Query(
			`SELECT id, user_id, api_key_id, model, backend, input_tokens, output_tokens, total_tokens,
			        cost_usd, status_code, latency_ms, is_openclaw, is_downgraded, ua, group_id, created_at
			 FROM usage_logs WHERE id > ? ORDER BY id LIMIT ?`, lastID, batchSize)
		if err != nil {
			return total, fmt.Errorf("查询 usage_logs: %w", err)
		}

		batchCount := 0
		var maxID int64

		tx, err := s.to.Begin()
		if err != nil {
			rows.Close()
			return total, fmt.Errorf("begin tx: %w", err)
		}

		stmt, err := tx.Prepare(pq.CopyIn("usage_logs",
			"user_id", "group_id", "api_key_id", "provider", "model", "backend_name",
			"input_tokens", "output_tokens", "total_tokens",
			"cache_read_tokens", "cache_write_tokens",
			"cost_usd", "status_code", "latency_ms",
			"is_openclaw", "is_downgraded", "ua", "created_at"))
		if err != nil {
			tx.Rollback()
			rows.Close()
			return total, fmt.Errorf("prepare copy: %w", err)
		}

		for rows.Next() {
			var (
				id         int64
				userID     int64
				apiKeyID   int64
				model      string
				backend    string
				inTok      int
				outTok     int
				totalTok   int
				costUSD    float64
				statusCode int
				latencyMs  int64
				isOC       int
				isDG       int
				ua         string
				groupID    int
				createdAt  string
			)
			if err := rows.Scan(&id, &userID, &apiKeyID, &model, &backend, &inTok, &outTok, &totalTok,
				&costUSD, &statusCode, &latencyMs, &isOC, &isDG, &ua, &groupID, &createdAt); err != nil {
				stmt.Close()
				tx.Rollback()
				rows.Close()
				return total, fmt.Errorf("scan: %w", err)
			}

			itcode, ok := sqliteUserMap[userID]
			if !ok {
				if id > maxID {
					maxID = id
				}
				continue
			}
			pgUID, ok := pgUserMap[itcode]
			if !ok {
				if id > maxID {
					maxID = id
				}
				continue
			}

			pgKeyID := mapKeyID(s.from, apiKeyID, pgKeyMap)
			provider, backendName := inferProvider(backend)

			if _, err := stmt.Exec(
				pgUID, groupID, pgKeyID, provider, model, backendName,
				inTok, outTok, totalTok, 0, 0,
				costUSD, statusCode, latencyMs,
				isOC != 0, isDG != 0, ua, parseTime(createdAt),
			); err != nil {
				log.Printf("    [警告] COPY usage_log id=%d 失败: %v", id, err)
			}

			if id > maxID {
				maxID = id
			}
			batchCount++
		}
		rows.Close()

		if batchCount == 0 {
			stmt.Close()
			tx.Rollback()
			break
		}

		if _, err := stmt.Exec(); err != nil {
			stmt.Close()
			tx.Rollback()
			return total, fmt.Errorf("flush copy: %w", err)
		}
		stmt.Close()

		if err := tx.Commit(); err != nil {
			return total, fmt.Errorf("commit: %w", err)
		}

		total += batchCount
		lastID = maxID
		if err := s.updateLastSyncedID("usage_logs", lastID); err != nil {
			return total, fmt.Errorf("更新同步进度: %w", err)
		}
		if time.Since(lastLog) >= 30*time.Second || batchCount < batchSize {
			elapsed := time.Since(startTime).Seconds()
			speed := float64(total) / elapsed
			pct := ""
			eta := ""
			if pendingCount > 0 {
				pct = fmt.Sprintf(" (%.1f%%)", float64(total)/float64(pendingCount)*100)
				if speed > 0 {
					remaining := float64(pendingCount-int64(total)) / speed
					eta = fmt.Sprintf(" ETA %.0fs", remaining)
				}
			}
			log.Printf("    进度: %d/%d%s  id=%d  %.0f条/s%s", total, pendingCount, pct, lastID, speed, eta)
			lastLog = time.Now()
		}

		if batchCount < batchSize {
			break
		}
	}
	return total, nil
}

// syncAWSUsageLogs incrementally syncs aws_usage_logs from SQLite to PG usage_logs with provider='aws'.
func (s *syncer) syncAWSUsageLogs() (int, error) {
	lastID, err := s.getLastSyncedID("aws_usage_logs")
	if err != nil {
		return 0, fmt.Errorf("获取同步进度: %w", err)
	}

	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, err
	}
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, err
	}
	pgKeyMap, err := s.buildPGKeyMap()
	if err != nil {
		return 0, err
	}

	var pendingCount int64
	s.from.QueryRow(`SELECT COUNT(*) FROM aws_usage_logs WHERE id > ?`, lastID).Scan(&pendingCount)
	if pendingCount > 0 {
		log.Printf("    待同步: %d 条 (从 id=%d 开始)", pendingCount, lastID)
	}

	total := 0
	startTime := time.Now()
	lastLog := time.Now()
	for {
		rows, err := s.from.Query(
			`SELECT id, user_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens,
			        cache_read_tokens, cache_write_tokens, cost_usd, status_code, latency_ms, ua, group_id, created_at
			 FROM aws_usage_logs WHERE id > ? ORDER BY id LIMIT ?`, lastID, batchSize)
		if err != nil {
			return total, fmt.Errorf("查询 aws_usage_logs: %w", err)
		}

		batchCount := 0
		var maxID int64

		tx, err := s.to.Begin()
		if err != nil {
			rows.Close()
			return total, fmt.Errorf("begin tx: %w", err)
		}

		stmt, err := tx.Prepare(pq.CopyIn("usage_logs",
			"user_id", "group_id", "api_key_id", "provider", "model", "backend_name",
			"input_tokens", "output_tokens", "total_tokens",
			"cache_read_tokens", "cache_write_tokens",
			"cost_usd", "status_code", "latency_ms",
			"is_openclaw", "is_downgraded", "ua", "created_at"))
		if err != nil {
			tx.Rollback()
			rows.Close()
			return total, fmt.Errorf("prepare copy: %w", err)
		}

		for rows.Next() {
			var (
				id           int64
				userID       int64
				apiKeyID     int64
				model        string
				bedrockModel string
				inTok        int
				outTok       int
				totalTok     int
				cacheRead    int
				cacheWrite   int
				costUSD      float64
				statusCode   int
				latencyMs    int64
				ua           string
				groupID      int
				createdAt    string
			)
			if err := rows.Scan(&id, &userID, &apiKeyID, &model, &bedrockModel, &inTok, &outTok, &totalTok,
				&cacheRead, &cacheWrite, &costUSD, &statusCode, &latencyMs, &ua, &groupID, &createdAt); err != nil {
				stmt.Close()
				tx.Rollback()
				rows.Close()
				return total, fmt.Errorf("scan: %w", err)
			}

			itcode, ok := sqliteUserMap[userID]
			if !ok {
				if id > maxID {
					maxID = id
				}
				continue
			}
			pgUID, ok := pgUserMap[itcode]
			if !ok {
				if id > maxID {
					maxID = id
				}
				continue
			}

			pgKeyID := mapKeyID(s.from, apiKeyID, pgKeyMap)

			if _, err := stmt.Exec(
				pgUID, groupID, pgKeyID, "aws", model, bedrockModel,
				inTok, outTok, totalTok, cacheRead, cacheWrite,
				costUSD, statusCode, latencyMs,
				false, false, ua, parseTime(createdAt),
			); err != nil {
				log.Printf("    [警告] COPY aws_usage_log id=%d 失败: %v", id, err)
			}

			if id > maxID {
				maxID = id
			}
			batchCount++
		}
		rows.Close()

		if batchCount == 0 {
			stmt.Close()
			tx.Rollback()
			break
		}

		if _, err := stmt.Exec(); err != nil {
			stmt.Close()
			tx.Rollback()
			return total, fmt.Errorf("flush copy: %w", err)
		}
		stmt.Close()

		if err := tx.Commit(); err != nil {
			return total, fmt.Errorf("commit: %w", err)
		}

		total += batchCount
		lastID = maxID
		if err := s.updateLastSyncedID("aws_usage_logs", lastID); err != nil {
			return total, fmt.Errorf("更新同步进度: %w", err)
		}
		if time.Since(lastLog) >= 30*time.Second || batchCount < batchSize {
			elapsed := time.Since(startTime).Seconds()
			speed := float64(total) / elapsed
			pct := ""
			eta := ""
			if pendingCount > 0 {
				pct = fmt.Sprintf(" (%.1f%%)", float64(total)/float64(pendingCount)*100)
				if speed > 0 {
					remaining := float64(pendingCount-int64(total)) / speed
					eta = fmt.Sprintf(" ETA %.0fs", remaining)
				}
			}
			log.Printf("    进度: %d/%d%s  id=%d  %.0f条/s%s", total, pendingCount, pct, lastID, speed, eta)
			lastLog = time.Now()
		}

		if batchCount < batchSize {
			break
		}
	}
	return total, nil
}

// syncDailyStats UPSERT daily_stats from SQLite to PG with provider='backend'.
func (s *syncer) syncDailyStats() (int, error) {
	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, err
	}
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, err
	}

	rows, err := s.from.Query(
		`SELECT id, date, user_id, model, requests, input_tokens, output_tokens, total_tokens, cost_usd
		 FROM daily_stats ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("查询 daily_stats: %w", err)
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var (
			id       int64
			date     string
			userID   int64
			model    string
			requests int
			inTok    int64
			outTok   int64
			totalTok int64
			costUSD  float64
		)
		if err := rows.Scan(&id, &date, &userID, &model, &requests, &inTok, &outTok, &totalTok, &costUSD); err != nil {
			return total, fmt.Errorf("scan daily_stats: %w", err)
		}

		itcode, ok := sqliteUserMap[userID]
		if !ok {
			continue
		}
		pgUID, ok := pgUserMap[itcode]
		if !ok {
			continue
		}

		_, err := s.to.Exec(
			`INSERT INTO daily_stats (date, user_id, provider, model, requests, input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, cost_usd)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, 0, $9)
			 ON CONFLICT (date, user_id, provider, model) DO UPDATE SET
			   requests = EXCLUDED.requests,
			   input_tokens = EXCLUDED.input_tokens,
			   output_tokens = EXCLUDED.output_tokens,
			   total_tokens = EXCLUDED.total_tokens,
			   cost_usd = EXCLUDED.cost_usd`,
			date, pgUID, "backend", model, requests, inTok, outTok, totalTok, costUSD,
		)
		if err != nil {
			log.Printf("    [警告] UPSERT daily_stats id=%d 失败: %v", id, err)
			continue
		}
		total++
	}
	return total, rows.Err()
}

// syncAWSDailyStats UPSERT aws_daily_stats from SQLite to PG daily_stats with provider='aws'.
func (s *syncer) syncAWSDailyStats() (int, error) {
	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, err
	}
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, err
	}

	rows, err := s.from.Query(
		`SELECT id, date, user_id, model, requests, input_tokens, output_tokens, total_tokens,
		        cache_read_tokens, cache_write_tokens, cost_usd
		 FROM aws_daily_stats ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("查询 aws_daily_stats: %w", err)
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var (
			id         int64
			date       string
			userID     int64
			model      string
			requests   int
			inTok      int64
			outTok     int64
			totalTok   int64
			cacheRead  int64
			cacheWrite int64
			costUSD    float64
		)
		if err := rows.Scan(&id, &date, &userID, &model, &requests, &inTok, &outTok, &totalTok,
			&cacheRead, &cacheWrite, &costUSD); err != nil {
			return total, fmt.Errorf("scan aws_daily_stats: %w", err)
		}

		itcode, ok := sqliteUserMap[userID]
		if !ok {
			continue
		}
		pgUID, ok := pgUserMap[itcode]
		if !ok {
			continue
		}

		_, err := s.to.Exec(
			`INSERT INTO daily_stats (date, user_id, provider, model, requests, input_tokens, output_tokens, total_tokens, cache_read_tokens, cache_write_tokens, cost_usd)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 ON CONFLICT (date, user_id, provider, model) DO UPDATE SET
			   requests = EXCLUDED.requests,
			   input_tokens = EXCLUDED.input_tokens,
			   output_tokens = EXCLUDED.output_tokens,
			   total_tokens = EXCLUDED.total_tokens,
			   cache_read_tokens = EXCLUDED.cache_read_tokens,
			   cache_write_tokens = EXCLUDED.cache_write_tokens,
			   cost_usd = EXCLUDED.cost_usd`,
			date, pgUID, "aws", model, requests, inTok, outTok, totalTok, cacheRead, cacheWrite, costUSD,
		)
		if err != nil {
			log.Printf("    [警告] UPSERT aws_daily_stats id=%d 失败: %v", id, err)
			continue
		}
		total++
	}
	return total, rows.Err()
}

// syncApplications UPSERT applications from SQLite to PG.
func (s *syncer) syncApplications() (int, error) {
	sqliteUserMap, err := s.buildSQLiteUserMap()
	if err != nil {
		return 0, err
	}
	pgUserMap, err := s.buildPGUserMap()
	if err != nil {
		return 0, err
	}

	rows, err := s.from.Query(
		`SELECT id, user_id, model, reason, status, reviewer_id, review_note, created_at, updated_at
		 FROM applications ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("查询 applications: %w", err)
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var (
			id         int64
			userID     int64
			model      string
			reason     string
			status     string
			reviewerID sql.NullInt64
			reviewNote string
			createdAt  string
			updatedAt  string
		)
		if err := rows.Scan(&id, &userID, &model, &reason, &status, &reviewerID, &reviewNote, &createdAt, &updatedAt); err != nil {
			return total, fmt.Errorf("scan applications: %w", err)
		}

		itcode, ok := sqliteUserMap[userID]
		if !ok {
			continue
		}
		pgUID, ok := pgUserMap[itcode]
		if !ok {
			continue
		}

		// Map reviewer_id if present
		var pgReviewerID interface{}
		if reviewerID.Valid {
			rItcode, ok := sqliteUserMap[reviewerID.Int64]
			if ok {
				if rPgID, ok := pgUserMap[rItcode]; ok {
					pgReviewerID = rPgID
				}
			}
		}

		_, err := s.to.Exec(
			`INSERT INTO applications (user_id, model, reason, status, reviewer_id, review_note, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT DO NOTHING`,
			pgUID, model, reason, status, pgReviewerID, reviewNote, parseTime(createdAt), parseTime(updatedAt),
		)
		if err != nil {
			log.Printf("    [警告] 插入 application id=%d 失败: %v", id, err)
			continue
		}
		total++
	}
	return total, rows.Err()
}

// --- Helper functions ---

// buildSQLiteUserMap returns SQLite user id -> itcode.
func (s *syncer) buildSQLiteUserMap() (map[int64]string, error) {
	rows, err := s.from.Query(`SELECT id, itcode FROM users`)
	if err != nil {
		return nil, fmt.Errorf("查询 SQLite 用户映射: %w", err)
	}
	defer rows.Close()
	m := make(map[int64]string)
	for rows.Next() {
		var id int64
		var itcode string
		if err := rows.Scan(&id, &itcode); err != nil {
			return nil, err
		}
		m[id] = itcode
	}
	return m, rows.Err()
}

// buildPGUserMap returns itcode -> PG users.id.
func (s *syncer) buildPGUserMap() (map[string]int64, error) {
	rows, err := s.to.Query(`SELECT id, itcode FROM users`)
	if err != nil {
		return nil, fmt.Errorf("查询 PG 用户映射: %w", err)
	}
	defer rows.Close()
	m := make(map[string]int64)
	for rows.Next() {
		var id int64
		var itcode string
		if err := rows.Scan(&id, &itcode); err != nil {
			return nil, err
		}
		m[itcode] = id
	}
	return m, rows.Err()
}

// buildPGKeyMap returns key string -> PG api_keys.id.
func (s *syncer) buildPGKeyMap() (map[string]int64, error) {
	rows, err := s.to.Query(`SELECT id, key FROM api_keys`)
	if err != nil {
		return nil, fmt.Errorf("查询 PG Key 映射: %w", err)
	}
	defer rows.Close()
	m := make(map[string]int64)
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		m[key] = id
	}
	return m, rows.Err()
}

// sqliteKeyCache caches SQLite api_key_id -> key string lookups.
var sqliteKeyCache = make(map[int64]string)

// mapKeyID maps a SQLite api_key_id to the corresponding PG api_keys.id.
func mapKeyID(fromDB *sql.DB, sqliteKeyID int64, pgKeyMap map[string]int64) int64 {
	keyStr, ok := sqliteKeyCache[sqliteKeyID]
	if !ok {
		err := fromDB.QueryRow(`SELECT key FROM api_keys WHERE id = ?`, sqliteKeyID).Scan(&keyStr)
		if err != nil {
			return 0
		}
		sqliteKeyCache[sqliteKeyID] = keyStr
	}
	pgID, ok := pgKeyMap[keyStr]
	if !ok {
		return 0
	}
	return pgID
}

// inferProvider determines provider and backend_name from the SQLite backend field.
func inferProvider(backend string) (provider, backendName string) {
	if strings.HasPrefix(backend, "public:kimi") {
		return "kimi", ""
	}
	if strings.HasPrefix(backend, "public:minimax") {
		return "minimax", ""
	}
	return "backend", backend
}

// parseTime attempts to parse various time formats from SQLite.
func parseTime(s string) time.Time {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05.000",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Now()
}

