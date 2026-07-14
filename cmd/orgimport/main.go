// cmd/orgimport 将 sky-insight 的组织/归属快照导入 gateway.db 的 users 表。
//
// 只按 itcode 匹配现有用户，UPDATE 归属相关列（mgr1_name / mgr2_name /
// attr_side / attr_group / is_departed），绝不触碰用量、Key、department/role_tag。
// 幂等：重复运行结果一致；快照更新后重跑即可刷新归属。
//
// 用法：
//
//	./bin/orgimport --db ./data/gateway.db \
//	  --map    ./sky-insight/data/attribution_map.json \
//	  --hier   ./sky-insight/data/org_hierarchy.csv \
//	  --config ./sky-insight/data/attribution_config.json
package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	_ "modernc.org/sqlite"
)

// attribution_map.json shape (only the fields we consume).
type attrMap struct {
	Members map[string]struct {
		Side  string `json:"side"`
		Group string `json:"group"`
		Mgr2  string `json:"mgr2"`
	} `json:"members"`
}

// attribution_config.json shape (only the fields we consume).
type attrConfig struct {
	ManualOverrides []struct {
		Itcode      string `json:"itcode"`
		TargetSide  string `json:"target_side"`
		TargetGroup string `json:"target_group"`
	} `json:"manual_overrides"`
	Exclusions []struct {
		Members []string `json:"members"`
	} `json:"exclusions"`
	Departed         []string `json:"departed"`
	SelfListedLeader *struct {
		Itcode string `json:"itcode"`
		Name   string `json:"name"`
	} `json:"self_listed_leader"`
}

// orgRow is the resolved attribution state for one itcode before it is written.
type orgRow struct {
	mgr1       string
	mgr2       string
	side       string
	group      string
	isDeparted bool
}

func main() {
	dbPath := flag.String("db", "./data/gateway.db", "目标数据库路径 (gateway.db)")
	mapPath := flag.String("map", "./sky-insight/data/attribution_map.json", "归属快照 attribution_map.json")
	hierPath := flag.String("hier", "./sky-insight/data/org_hierarchy.csv", "主管链 org_hierarchy.csv")
	cfgPath := flag.String("config", "./sky-insight/data/attribution_config.json", "归口业务规则 attribution_config.json")
	flag.Parse()

	rows, err := buildRows(*mapPath, *hierPath, *cfgPath)
	if err != nil {
		log.Fatalf("解析快照失败: %v", err)
	}
	log.Printf("==> 解析快照完成：%d 个 itcode 待导入", len(rows))

	db, err := openDB(*dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	matched, missing, err := apply(db, rows)
	if err != nil {
		log.Fatalf("导入失败: %v", err)
	}

	log.Printf("==> 导入完成：匹配 %d, 未匹配(库中无此 itcode) %d", matched, len(missing))
	if len(missing) > 0 {
		sort.Strings(missing)
		log.Printf("    未匹配 itcode（快照有、users 表无，可能新账号/离职未建号）：")
		for _, it := range missing {
			log.Printf("      - %s", it)
		}
	}
}

// buildRows merges map + hierarchy + config into the final per-itcode state.
func buildRows(mapPath, hierPath, cfgPath string) (map[string]*orgRow, error) {
	var amap attrMap
	if err := readJSON(mapPath, &amap); err != nil {
		return nil, fmt.Errorf("读 map: %w", err)
	}
	var cfg attrConfig
	if err := readJSON(cfgPath, &cfg); err != nil {
		return nil, fmt.Errorf("读 config: %w", err)
	}
	hier, err := readHierarchy(hierPath)
	if err != nil {
		return nil, fmt.Errorf("读 hierarchy: %w", err)
	}

	rows := map[string]*orgRow{}
	get := func(itcode string) *orgRow {
		r := rows[itcode]
		if r == nil {
			r = &orgRow{}
			rows[itcode] = r
		}
		return r
	}

	// 1. 快照成员 → attr_side / attr_group / mgr2
	for itcode, m := range amap.Members {
		r := get(itcode)
		r.side = m.Side
		r.group = m.Group
		if m.Mgr2 != "" {
			r.mgr2 = m.Mgr2
		}
	}

	// 2. 主管链 CSV → mgr1_name / mgr2_name（CSV 的 mgr2 优先，语义更权威）
	for itcode, h := range hier {
		r := get(itcode)
		r.mgr1 = h.mgr1
		if h.mgr2 != "" {
			r.mgr2 = h.mgr2
		}
	}

	// 3a. self_listed_leader：本人特判为独立负责人组（shen 侧）
	if sl := cfg.SelfListedLeader; sl != nil && sl.Itcode != "" {
		r := get(sl.Itcode)
		r.side = "shen"
		r.group = sl.Name
	}

	// 3b. manual_overrides：覆盖归属
	for _, mo := range cfg.ManualOverrides {
		if mo.Itcode == "" {
			continue
		}
		r := get(mo.Itcode)
		r.side = mo.TargetSide
		r.group = mo.TargetGroup
	}

	// 3c. exclusions：整组剔除归属（清空 side/group，不纳入归口统计）
	for _, ex := range cfg.Exclusions {
		for _, itcode := range ex.Members {
			r := get(itcode)
			r.side = ""
			r.group = ""
		}
	}

	// 3d. departed：离职标记（与归属正交，仍保留其组信息）
	for _, itcode := range cfg.Departed {
		get(itcode).isDeparted = true
	}

	return rows, nil
}

// apply UPDATEs each itcode's org columns in a single transaction.
// Returns matched count and the list of itcodes not found in users.
func apply(db *sql.DB, rows map[string]*orgRow) (int, []string, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`UPDATE users SET mgr1_name=?, mgr2_name=?, attr_side=?, attr_group=?, is_departed=?
		 WHERE itcode=?`)
	if err != nil {
		return 0, nil, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	matched := 0
	var missing []string
	for itcode, r := range rows {
		departed := 0
		if r.isDeparted {
			departed = 1
		}
		res, err := stmt.Exec(r.mgr1, r.mgr2, r.side, r.group, departed, itcode)
		if err != nil {
			return 0, nil, fmt.Errorf("update %s: %w", itcode, err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			matched++
		} else {
			missing = append(missing, itcode)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	return matched, missing, nil
}

type hierEntry struct {
	mgr1 string
	mgr2 string
}

// readHierarchy parses org_hierarchy.csv (header: itcode,mgr1_name,mgr1_title,mgr2_name).
func readHierarchy(path string) (map[string]hierEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	out := map[string]hierEntry{}
	for i, rec := range records {
		if i == 0 || len(rec) < 4 { // skip header / short rows
			continue
		}
		itcode := rec[0]
		if itcode == "" {
			continue
		}
		out[itcode] = hierEntry{mgr1: rec[1], mgr2: rec[3]}
	}
	return out, nil
}

func readJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
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
