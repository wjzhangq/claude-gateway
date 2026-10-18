package db

import (
	"os"
	"testing"
	"time"
)

// BenchmarkDateQuery compares SUBSTR vs range query performance.
// Run with: go test -bench=BenchmarkDateQuery -benchmem ./internal/db
func BenchmarkDateQuery(b *testing.B) {
	tmpDB := "/tmp/test_query_perf.db"
	os.Remove(tmpDB)
	defer os.Remove(tmpDB)

	d, err := Init(tmpDB, 1000)
	if err != nil {
		b.Fatalf("init db: %v", err)
	}
	defer d.Close()

	// Insert test data
	now := time.Now()
	for i := 0; i < 1000; i++ {
		ts := now.Add(-time.Duration(i) * time.Hour).Format("2006-01-02 15:04:05")
		_, err := d.db.Exec(`INSERT INTO usage_logs (user_id, api_key_id, model, created_at) VALUES (?, 1, 'test-model', ?)`,
			i%10+1, ts)
		if err != nil {
			b.Fatalf("insert: %v", err)
		}
	}

	date := now.Format("2006-01-02")
	startTime := date + " 00:00:00"
	endTime := date + " 23:59:59.999"

	b.Run("SUBSTR", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := d.db.Query(`SELECT user_id, COUNT(*) FROM usage_logs WHERE SUBSTR(created_at, 1, 10) = ? GROUP BY user_id`, date)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				var uid int64
				var cnt int
				rows.Scan(&uid, &cnt)
			}
			rows.Close()
		}
	})

	b.Run("RangeQuery", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := d.db.Query(`SELECT user_id, COUNT(*) FROM usage_logs WHERE created_at >= ? AND created_at <= ? GROUP BY user_id`,
				startTime, endTime)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				var uid int64
				var cnt int
				rows.Scan(&uid, &cnt)
			}
			rows.Close()
		}
	})
}
