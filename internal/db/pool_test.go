package db

import (
	"path/filepath"
	"testing"
	"time"
)

// TestQueryReadNotBlockedByWriteTx is the regression test for the slow-query
// pile-up: the write pool holds a single connection (SetMaxOpenConns(1)), so any
// read issued through it queues behind an in-flight write. That queue wait is
// counted inside the measured duration, which is why unrelated statements showed
// up in the slow-query log with near-identical multi-second timings. Reads routed
// to the read-only pool must not wait on the writer.
func TestQueryReadNotBlockedByWriteTx(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool.db")
	d, err := Init(dbPath, 1000)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(`INSERT INTO users (itcode, name) VALUES ('alice', 'A')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// Take the one and only write connection and keep it busy.
	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO users (itcode, name) VALUES ('bob', 'B')`); err != nil {
		t.Fatalf("tx insert: %v", err)
	}

	// The read-only pool must answer while that transaction is still open, and
	// must see the last committed state (alice only, not the uncommitted bob).
	type result struct {
		n   int
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		var n int
		err := d.QueryRowRead(`SELECT COUNT(*) FROM users`).Scan(&n)
		readDone <- result{n, err}
	}()

	select {
	case r := <-readDone:
		if r.err != nil {
			t.Fatalf("QueryRowRead during open write tx: %v", r.err)
		}
		if r.n != 1 {
			t.Errorf("QueryRowRead saw %d users, want 1 (committed state only)", r.n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("QueryRowRead blocked by an open write transaction")
	}

	// Same read through the write pool is expected to queue behind the tx. This
	// asserts the mechanism the fix routes around; if it ever stops blocking the
	// pool configuration changed and the read split needs rethinking.
	writeDone := make(chan struct{})
	go func() {
		var n int
		_ = d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
		close(writeDone)
	}()

	select {
	case <-writeDone:
		t.Error("read through the write pool completed while a write tx held the only connection; pool assumptions changed")
	case <-time.After(500 * time.Millisecond):
		// Expected: still queued.
	}
}

// TestQueryReadRows checks the multi-row read helper on the read-only pool.
func TestQueryReadRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool_rows.db")
	d, err := Init(dbPath, 1000)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer d.Close()

	for _, itcode := range []string{"u1", "u2", "u3"} {
		if _, err := d.Exec(`INSERT INTO users (itcode, name) VALUES (?, '')`, itcode); err != nil {
			t.Fatalf("seed %s: %v", itcode, err)
		}
	}

	rows, err := d.QueryRead(`SELECT itcode FROM users ORDER BY itcode`)
	if err != nil {
		t.Fatalf("QueryRead: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(got) != 3 || got[0] != "u1" || got[2] != "u3" {
		t.Errorf("QueryRead returned %v, want [u1 u2 u3]", got)
	}
}

// TestQueryReadRejectsWrites confirms the read-only pool is enforced at the
// driver level, so a stray write routed to it fails loudly instead of silently
// bypassing write serialization.
func TestQueryReadRejectsWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool_ro.db")
	d, err := Init(dbPath, 1000)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer d.Close()

	rows, err := d.QueryRead(`INSERT INTO users (itcode, name) VALUES ('nope', '')`)
	if err == nil {
		rows.Close()
		t.Fatal("write through the read-only pool succeeded, want an error")
	}
}
