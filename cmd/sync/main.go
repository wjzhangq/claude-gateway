// cmd/sync 将旧数据库（database.db）中的用户、API Key 和用量同步到本项目数据库（gateway.db）。
// 支持多次重入：已存在的记录会被更新，用户和 Key 一定保留，统计数据尽量保留。
//
// 用法：
//
//	./bin/sync --fromdb ./data/database.db --todb ./data/gateway.db
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	fromPath := flag.String("fromdb", "./data/database.db", "源数据库路径（旧 database.db）")
	toPath := flag.String("todb", "./data/gateway.db", "目标数据库路径（本项目 gateway.db）")
	flag.Parse()

	if *fromPath == "" || *toPath == "" {
		fmt.Fprintln(os.Stderr, "用法: sync --fromdb <源DB> --todb <目标DB>")
		os.Exit(1)
	}

	fromDB, err := openDB(*fromPath)
	if err != nil {
		log.Fatalf("打开源数据库失败: %v", err)
	}
	defer fromDB.Close()

	toDB, err := openDB(*toPath)
	if err != nil {
		log.Fatalf("打开目标数据库失败: %v", err)
	}
	defer toDB.Close()

	s := &syncer{from: fromDB, to: toDB}

	log.Println("==> 同步用户...")
	inserted, updated, err := s.syncUsers()
	if err != nil {
		log.Fatalf("同步用户失败: %v", err)
	}
	log.Printf("    新增: %d, 更新: %d", inserted, updated)

	log.Println("==> 同步 API Key...")
	inserted, updated, err = s.syncAPIKeys()
	if err != nil {
		log.Fatalf("同步 API Key 失败: %v", err)
	}
	log.Printf("    新增: %d, 更新: %d", inserted, updated)

	log.Println("==> 同步用量（daily_stats）...")
	inserted, updated, err = s.syncUsage()
	if err != nil {
		log.Fatalf("同步用量失败: %v", err)
	}
	log.Printf("    新增: %d, 更新: %d", inserted, updated)

	log.Println("==> 同步完成")
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

type syncer struct {
	from *sql.DB
	to   *sql.DB
}

// syncUsers 同步用户，返回 (新增, 更新) 数量。
// UPSERT: 已存在的用户更新 role/status/quota_tokens；name 仅在目标为空时才写入。
func (s *syncer) syncUsers() (int, int, error) {
	rows, err := s.from.Query(
		`SELECT user_id, itcode, is_admin, is_active, max_token, add_date, update_date FROM user`)
	if err != nil {
		return 0, 0, fmt.Errorf("查询源用户: %w", err)
	}
	defer rows.Close()

	type fromUser struct {
		UserID     string
		Itcode     string
		IsAdmin    int
		IsActive   int
		MaxToken   int64
		AddDate    string
		UpdateDate string
	}

	var users []fromUser
	for rows.Next() {
		var u fromUser
		if err := rows.Scan(&u.UserID, &u.Itcode, &u.IsAdmin, &u.IsActive, &u.MaxToken, &u.AddDate, &u.UpdateDate); err != nil {
			return 0, 0, fmt.Errorf("读取源用户行: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	inserted, updated := 0, 0
	for _, u := range users {
		role := "user"
		if u.IsAdmin == 1 {
			role = "admin"
		}
		status := "disabled"
		if u.IsActive == 1 {
			status = "active"
		}

		// 先检查是否已存在
		var existID int64
		err := s.to.QueryRow(`SELECT id FROM users WHERE itcode = ?`, u.Itcode).Scan(&existID)
		if err == sql.ErrNoRows {
			// 新增
			_, err = s.to.Exec(
				`INSERT INTO users (itcode, name, role, status, quota_tokens, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				u.Itcode, u.Itcode, role, status, u.MaxToken, u.AddDate, u.UpdateDate,
			)
			if err != nil {
				log.Printf("    [警告] 插入用户 %s 失败: %v", u.Itcode, err)
				continue
			}
			inserted++
		} else if err == nil {
			// 更新：保留目标库中已有的 name（如果非空），更新其他字段
			_, err = s.to.Exec(
				`UPDATE users SET role=?, status=?, quota_tokens=?, updated_at=?
				 WHERE id=?`,
				role, status, u.MaxToken, u.UpdateDate, existID,
			)
			if err != nil {
				log.Printf("    [警告] 更新用户 %s 失败: %v", u.Itcode, err)
				continue
			}
			updated++
		} else {
			log.Printf("    [警告] 查询用户 %s 失败: %v", u.Itcode, err)
		}
	}
	return inserted, updated, nil
}

// syncAPIKeys 同步 API Key，返回 (新增, 更新) 数量。
func (s *syncer) syncAPIKeys() (int, int, error) {
	itcodeToToID, err := s.buildItcodeMap()
	if err != nil {
		return 0, 0, err
	}
	fromUserMap, err := s.buildFromUserMap()
	if err != nil {
		return 0, 0, err
	}

	rows, err := s.from.Query(
		`SELECT id, user_id, apikey, alias, expire_date, add_time, is_active FROM api_key`)
	if err != nil {
		return 0, 0, fmt.Errorf("查询源 API Key: %w", err)
	}
	defer rows.Close()

	type fromAPIKey struct {
		ID         string
		UserID     string
		Apikey     string
		Alias      sql.NullString
		ExpireDate sql.NullString
		AddTime    string
		IsActive   int
	}

	var keys []fromAPIKey
	for rows.Next() {
		var k fromAPIKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Apikey, &k.Alias, &k.ExpireDate, &k.AddTime, &k.IsActive); err != nil {
			return 0, 0, fmt.Errorf("读取源 API Key 行: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	inserted, updated := 0, 0
	for _, k := range keys {
		itcode, ok := fromUserMap[k.UserID]
		if !ok {
			log.Printf("    [警告] API Key %s 的 user_id %s 在源库中找不到对应用户，跳过", k.Apikey[:16]+"...", k.UserID)
			continue
		}
		toUserID, ok := itcodeToToID[itcode]
		if !ok {
			log.Printf("    [警告] API Key 的用户 %s 在目标库中不存在，跳过", itcode)
			continue
		}

		status := "disabled"
		if k.IsActive == 1 {
			status = "active"
		}
		name := ""
		if k.Alias.Valid {
			name = k.Alias.String
		}
		var expiresAt interface{}
		if k.ExpireDate.Valid && k.ExpireDate.String != "" {
			expiresAt = k.ExpireDate.String
		}

		// 先检查是否已存在
		var existID int64
		err := s.to.QueryRow(`SELECT id FROM api_keys WHERE key = ?`, k.Apikey).Scan(&existID)
		if err == sql.ErrNoRows {
			// 新增
			_, err = s.to.Exec(
				`INSERT INTO api_keys (user_id, key, name, status, expires_at, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				toUserID, k.Apikey, name, status, expiresAt, k.AddTime, k.AddTime,
			)
			if err != nil {
				log.Printf("    [警告] 插入 API Key 失败: %v", err)
				continue
			}
			inserted++
		} else if err == nil {
			// 更新：同步 status，保留目标库中已有的 name
			_, err = s.to.Exec(
				`UPDATE api_keys SET user_id=?, status=?, expires_at=?, updated_at=?
				 WHERE id=?`,
				toUserID, status, expiresAt, k.AddTime, existID,
			)
			if err != nil {
				log.Printf("    [警告] 更新 API Key 失败: %v", err)
				continue
			}
			updated++
		} else {
			log.Printf("    [警告] 查询 API Key 失败: %v", err)
		}
	}
	return inserted, updated, nil
}

// syncUsage 将 fromdb.usage 同步到 todb.daily_stats，返回 (新增, 更新) 数量。
// UPSERT: 已存在的记录会被更新。
func (s *syncer) syncUsage() (int, int, error) {
	itcodeToToID, err := s.buildItcodeMap()
	if err != nil {
		return 0, 0, err
	}
	fromUserMap, err := s.buildFromUserMap()
	if err != nil {
		return 0, 0, err
	}

	rows, err := s.from.Query(
		`SELECT user_id, day, use_token, use_cost FROM usage`)
	if err != nil {
		return 0, 0, fmt.Errorf("查询源用量: %w", err)
	}
	defer rows.Close()

	type fromUsage struct {
		UserID   string
		Day      string
		UseToken int64
		UseCost  float64
	}

	var usages []fromUsage
	for rows.Next() {
		var u fromUsage
		if err := rows.Scan(&u.UserID, &u.Day, &u.UseToken, &u.UseCost); err != nil {
			return 0, 0, fmt.Errorf("读取源用量行: %w", err)
		}
		usages = append(usages, u)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	inserted, updated := 0, 0
	for _, u := range usages {
		itcode, ok := fromUserMap[u.UserID]
		if !ok {
			continue
		}
		toUserID, ok := itcodeToToID[itcode]
		if !ok {
			continue
		}

		// UPSERT: model 用 'synced' 标识来源于旧系统
		res, err := s.to.Exec(
			`INSERT INTO daily_stats (date, user_id, model, requests, input_tokens, output_tokens, total_tokens, cost_usd)
			 VALUES (?, ?, 'synced', 0, 0, 0, ?, ?)
			 ON CONFLICT(date, user_id, model) DO UPDATE SET
			   total_tokens = excluded.total_tokens,
			   cost_usd     = excluded.cost_usd`,
			u.Day, toUserID, u.UseToken, u.UseCost,
		)
		if err != nil {
			log.Printf("    [警告] 插入/更新用量 %s/%s 失败: %v", itcode, u.Day, err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			// SQLite ON CONFLICT DO UPDATE 总是返回 1，无法区分 insert/update
			// 简单计数
			inserted++
		}
	}
	// 由于 SQLite upsert 无法精确区分 insert/update，这里 inserted 实际是总处理数
	return inserted, updated, nil
}

// buildFromUserMap 返回 fromdb user_id (TEXT) -> itcode 的映射
func (s *syncer) buildFromUserMap() (map[string]string, error) {
	rows, err := s.from.Query(`SELECT user_id, itcode FROM user`)
	if err != nil {
		return nil, fmt.Errorf("查询源用户映射: %w", err)
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var uid, itcode string
		if err := rows.Scan(&uid, &itcode); err != nil {
			return nil, err
		}
		m[uid] = itcode
	}
	return m, rows.Err()
}

// buildItcodeMap 返回 itcode -> todb users.id (int64) 的映射
func (s *syncer) buildItcodeMap() (map[string]int64, error) {
	rows, err := s.to.Query(`SELECT id, itcode FROM users`)
	if err != nil {
		return nil, fmt.Errorf("查询目标用户映射: %w", err)
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
