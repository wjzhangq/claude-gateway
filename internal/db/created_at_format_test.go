package db

import (
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/model"
)

// TestCreatedAtStorageFormat locks the on-disk shape of usage_logs.created_at.
//
// Every "today" query in the admin docs and the DB-explorer skill filters with
// a YYYY-MM-DD prefix comparison (created_at >= date('now','+8 hours')). That
// only works because created_at is stored with a leading YYYY-MM-DD in the
// server's local zone. It also must stay parseable by SQLite's date(): an
// earlier doc recorded the format as Go's time.String()
// ("... +0800 CST m=+61.8"), which date() cannot parse and silently returns
// NULL for. If the driver or insert path ever changes the format, this test
// fails instead of letting the reporting queries go quietly wrong.
func TestCreatedAtStorageFormat(t *testing.T) {
	d, err := Init(filepath.Join(t.TempDir(), "tz.db"), 1000)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer d.Close()

	u := &model.User{Itcode: "tzuser", Name: "TZ", Role: "user", Status: "active"}
	if err := d.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := d.BatchInsertUsageLogs([]*model.UsageLog{{
		UserID: u.ID, Model: "m", Backend: "b", CostUSD: 1, StatusCode: 200,
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// CAST(... AS TEXT) is required: scanning the column straight into a Go
	// string lets the driver reformat it as RFC3339, hiding what is on disk.
	var raw string
	if err := d.QueryRowRead(
		`SELECT CAST(created_at AS TEXT) FROM usage_logs LIMIT 1`).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	t.Logf("stored created_at = %q (local zone %s)", raw, time.Now().Format("-0700 MST"))

	// 1. Leading YYYY-MM-DD, so lexicographic prefix filtering works.
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`).MatchString(raw) {
		t.Errorf("created_at %q does not start with YYYY-MM-DD; prefix-range queries would break", raw)
	}

	// 2. The date prefix is the local date, which is what date('now','+8 hours')
	//    yields on a +0800 server. A UTC-stored prefix would shift the day.
	if want := time.Now().Format("2006-01-02"); raw[:len(want)] != want {
		t.Errorf("created_at date prefix = %q, want local date %q", raw[:len(want)], want)
	}

	// 3. date()/datetime() CANNOT parse this format and yield NULL. Locked in
	//    deliberately: reporting queries must use prefix/range comparison, not
	//    date(created_at). If a future driver starts parsing it, this fails and
	//    the docs can be relaxed.
	for _, expr := range []string{"date(created_at)", "datetime(created_at)"} {
		var parsed *string
		if err := d.QueryRowRead(`SELECT ` + expr + ` FROM usage_logs LIMIT 1`).Scan(&parsed); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if parsed != nil {
			t.Errorf("%s = %q, expected NULL for the Go time.String() storage format;"+
				" docs may now allow date() on created_at", expr, *parsed)
		}
	}

	// 4. The prefix comparison the docs actually tell people to use selects the row.
	var n int
	if err := d.QueryRowRead(
		`SELECT COUNT(*) FROM usage_logs WHERE created_at >= ?`,
		time.Now().Format("2006-01-02"),
	).Scan(&n); err != nil {
		t.Fatalf("prefix filter: %v", err)
	}
	if n != 1 {
		t.Errorf("prefix filter on local today matched %d rows, want 1", n)
	}
}
