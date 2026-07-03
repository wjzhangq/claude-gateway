//go:build ignore

// import_org_tags.go — one-off migration: import sky-insight's user_org.json
// into the users table (department / role_tag / org_note columns).
//
// The JSON is keyed by user_id, matching sky-insight/backend/app/org_store.py:
//   { "<user_id>": {"department": "...", "role_tag": "...", "note": "..."}, ... }
//
// Usage:
//   go run scripts/import_org_tags.go [-db data/gateway.db] [-json sky-insight/data/user_org.json] [-dry]
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

type orgTag struct {
	Department string `json:"department"`
	RoleTag    string `json:"role_tag"`
	Note       string `json:"note"`
}

func main() {
	dbPath := flag.String("db", "data/gateway.db", "path to gateway SQLite database")
	jsonPath := flag.String("json", "sky-insight/data/user_org.json", "path to user_org.json")
	dry := flag.Bool("dry", false, "print planned updates without writing")
	flag.Parse()

	raw, err := os.ReadFile(*jsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read json: %v\n", err)
		os.Exit(1)
	}
	var org map[string]orgTag
	if err := json.Unmarshal(raw, &org); err != nil {
		fmt.Fprintf(os.Stderr, "parse json: %v\n", err)
		os.Exit(1)
	}
	if len(org) == 0 {
		fmt.Println("user_org.json is empty — nothing to import.")
		return
	}

	db, err := sql.Open("sqlite", *dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	now := time.Now()
	updated, missing, skipped := 0, 0, 0
	for uidStr, tag := range org {
		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip invalid user_id %q\n", uidStr)
			skipped++
			continue
		}
		roleTag := tag.RoleTag
		if roleTag == "" {
			roleTag = "未分类"
		}

		// Verify the user exists before updating.
		var itcode string
		err = db.QueryRow(`SELECT itcode FROM users WHERE id = ?`, uid).Scan(&itcode)
		if err == sql.ErrNoRows {
			fmt.Fprintf(os.Stderr, "user_id %d not found in DB — skipped\n", uid)
			missing++
			continue
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "query user %d: %v\n", uid, err)
			os.Exit(1)
		}

		if *dry {
			fmt.Printf("[dry] %d (%s): dept=%q role=%q note=%q\n", uid, itcode, tag.Department, roleTag, tag.Note)
			updated++
			continue
		}

		_, err = db.Exec(
			`UPDATE users SET department=?, role_tag=?, org_note=?, updated_at=? WHERE id=?`,
			tag.Department, roleTag, tag.Note, now, uid,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update user %d: %v\n", uid, err)
			os.Exit(1)
		}
		updated++
	}

	fmt.Printf("Done. updated=%d missing=%d skipped=%d (dry=%v)\n", updated, missing, skipped, *dry)
}
