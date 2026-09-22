package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/logger"
)

func TestSlowQueryLogging(t *testing.T) {
	// Setup temp log directory
	logDir := t.TempDir()
	logger.Init("info", "json")
	logger.InitSlowQueryLog(logDir)

	// Create test DB with 0ms threshold (log everything for testing)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	d, err := Init(dbPath, 0)
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer d.Close()

	// Execute a simple query - should be logged since threshold is 0
	start := time.Now()
	_, err = d.Exec("SELECT 1")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("exec query: %v", err)
	}
	t.Logf("Query took %v", elapsed)

	// Give logger time to flush
	time.Sleep(100 * time.Millisecond)

	// Check if slow query log file was created
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}

	var slowQueryLog string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "slow-query-") && strings.HasSuffix(e.Name(), ".log") {
			slowQueryLog = filepath.Join(logDir, e.Name())
			break
		}
	}

	if slowQueryLog == "" {
		t.Fatal("slow query log file not created")
	}

	// Read log content
	content, err := os.ReadFile(slowQueryLog)
	if err != nil {
		t.Fatalf("read slow query log: %v", err)
	}

	logContent := string(content)
	t.Logf("Slow query log content:\n%s", logContent)

	if logContent == "" {
		t.Fatal("slow query log is empty")
	}

	if !strings.Contains(logContent, "duration_ms") {
		t.Errorf("slow query log missing duration_ms field")
	}
	if !strings.Contains(logContent, "sql") {
		t.Errorf("slow query log missing sql field")
	}
	if !strings.Contains(logContent, "caller") {
		t.Errorf("slow query log missing caller field")
	}
}
