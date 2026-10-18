package db

import (
	"os"
	"testing"
	"time"
)

// TestAggregationPerformance tests the real aggregation functions with sample data.
func TestAggregationPerformance(t *testing.T) {
	tmpDB := "/tmp/test_aggregation.db"
	os.Remove(tmpDB)
	defer os.Remove(tmpDB)

	d, err := Init(tmpDB, 1000)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer d.Close()

	// Insert 10k records spanning 3 days
	now := time.Now()
	today := now.Format("2006-01-02")

	t.Logf("Inserting test data...")
	for i := 0; i < 10000; i++ {
		dayOffset := i % 3 // 0=today, 1=yesterday, 2=day before
		ts := now.AddDate(0, 0, -dayOffset).Format("2006-01-02 15:04:05.000")
		_, err := d.db.Exec(`
			INSERT INTO usage_logs
			(user_id, api_key_id, model, input_tokens, output_tokens, total_tokens, cost_usd, created_at)
			VALUES (?, 1, ?, 1000, 500, 1500, 0.05, ?)`,
			(i%50)+1, // 50 users
			"claude-opus-5-5",
			ts,
		)
		if err != nil {
			t.Fatalf("insert usage_logs: %v", err)
		}

		_, err = d.db.Exec(`
			INSERT INTO aws_usage_logs
			(user_id, api_key_id, model, bedrock_model, input_tokens, output_tokens, total_tokens,
			 cache_read_tokens, cache_write_tokens, cost_usd, created_at)
			VALUES (?, 1, ?, ?, 800, 400, 1200, 200, 100, 0.04, ?)`,
			(i%50)+1,
			"claude-opus-5-5",
			"us.anthropic.claude-3-5-sonnet-20240620-v1:0",
			ts,
		)
		if err != nil {
			t.Fatalf("insert aws_usage_logs: %v", err)
		}
	}

	// Test aggregateForDate performance
	start := time.Now()
	if err := d.aggregateForDate(today); err != nil {
		t.Fatalf("aggregateForDate: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("aggregateForDate(%s): %v", today, elapsed)

	// Test aggregateAWSForDate performance
	start = time.Now()
	if err := d.aggregateAWSForDate(today); err != nil {
		t.Fatalf("aggregateAWSForDate: %v", err)
	}
	elapsed = time.Since(start)
	t.Logf("aggregateAWSForDate(%s): %v", today, elapsed)

	// Verify results
	var count int
	d.db.QueryRow("SELECT COUNT(*) FROM daily_stats WHERE date = ?", today).Scan(&count)
	t.Logf("daily_stats records for %s: %d", today, count)

	d.db.QueryRow("SELECT COUNT(*) FROM aws_daily_stats WHERE date = ?", today).Scan(&count)
	t.Logf("aws_daily_stats records for %s: %d", today, count)
}
