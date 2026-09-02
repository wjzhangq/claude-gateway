package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
)

// DBExplorerHandler serves database schema and query endpoints.
type DBExplorerHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
	config   *config.Config
}

func NewDBExplorerHandler(database *db.DB, keyStore *auth.KeyStore, cfg *config.Config) *DBExplorerHandler {
	return &DBExplorerHandler{db: database, keyStore: keyStore, config: cfg}
}

// AdminAPIKeyAuth middleware validates that the request carries an admin API key.
func (h *DBExplorerHandler) AdminAPIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			raw = c.GetHeader("x-api-key")
			if raw == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing api key"})
				return
			}
		} else {
			raw = strings.TrimPrefix(raw, "Bearer ")
			raw = strings.TrimPrefix(raw, "bearer ")
		}

		info := h.keyStore.Get(raw)
		if info == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
			return
		}
		if info.UserStatus != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "user is disabled"})
			return
		}

		// Check user is admin
		user, err := h.db.GetUserByID(info.UserID)
		if err != nil || user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}
		if user.Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin required"})
			return
		}

		c.Next()
	}
}

// GetSchema handles GET /admin/api/db/schema
func (h *DBExplorerHandler) GetSchema(c *gin.Context) {
	schema, err := h.db.GetSchema()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tables": schema})
}

// ExecuteQuery handles POST /admin/api/db/query
func (h *DBExplorerHandler) ExecuteQuery(c *gin.Context) {
	var req struct {
		SQL string `json:"sql" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sql field is required"})
		return
	}

	if strings.TrimSpace(req.SQL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sql cannot be empty"})
		return
	}

	result, err := h.db.ExecuteReadQuery(req.SQL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// GetRecentErrors handles GET /admin/api/db/errors/recent - reads from error-YYYY-MM-DD.log
func (h *DBExplorerHandler) GetRecentErrors(c *gin.Context) {
	limit := parseIntParam(c, "limit", 100)
	if limit > 1000 {
		limit = 1000
	}

	logDir := h.config.Log.Dir
	if logDir == "" {
		c.JSON(http.StatusOK, gin.H{
			"logs":  []interface{}{},
			"count": 0,
			"error": "log directory not configured",
		})
		return
	}

	today := time.Now().Format("2006-01-02")
	filename := filepath.Join(logDir, fmt.Sprintf("error-%s.log", today))

	logs, err := readLogFileReversed(filename, limit)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{
				"logs":  []interface{}{},
				"count": 0,
				"date":  today,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  logs,
		"count": len(logs),
		"date":  today,
	})
}

// GetRecentLogs handles GET /admin/api/db/logs/recent - reads from backend-YYYY-MM-DD.log and dasheng-YYYY-MM-DD.log
func (h *DBExplorerHandler) GetRecentLogs(c *gin.Context) {
	limit := parseIntParam(c, "limit", 100)
	if limit > 1000 {
		limit = 1000
	}

	logDir := h.config.Log.Dir
	if logDir == "" {
		c.JSON(http.StatusOK, gin.H{
			"logs":  []interface{}{},
			"count": 0,
			"error": "log directory not configured",
		})
		return
	}

	today := time.Now().Format("2006-01-02")
	backendFile := filepath.Join(logDir, fmt.Sprintf("backend-%s.log", today))
	dashengFile := filepath.Join(logDir, fmt.Sprintf("dasheng-%s.log", today))

	// Read both files
	backendLogs, err1 := readLogFileReversed(backendFile, limit*2)
	dashengLogs, err2 := readLogFileReversed(dashengFile, limit*2)

	// If both files don't exist, return empty
	if os.IsNotExist(err1) && os.IsNotExist(err2) {
		c.JSON(http.StatusOK, gin.H{
			"logs":  []interface{}{},
			"count": 0,
			"date":  today,
		})
		return
	}

	// Merge logs
	allLogs := mergeAndSortLogs(backendLogs, dashengLogs, err1, err2)

	// Apply limit
	if len(allLogs) > limit {
		allLogs = allLogs[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  allLogs,
		"count": len(allLogs),
		"date":  today,
	})
}

func parseIntParam(c *gin.Context, key string, defaultVal int) int {
	val := c.Query(key)
	if val == "" {
		return defaultVal
	}
	var result int
	if _, err := fmt.Sscanf(val, "%d", &result); err != nil {
		return defaultVal
	}
	if result < 0 {
		return 0
	}
	return result
}

// readLogFileReversed reads up to limit lines from the end of a JSON Lines log file
func readLogFileReversed(filename string, limit int) ([]map[string]interface{}, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := stat.Size()

	// Read from end in chunks to find last N lines
	const chunkSize = 8192
	var lines []string
	var buffer []byte
	pos := fileSize

	for len(lines) < limit && pos > 0 {
		// Calculate how much to read
		readSize := int64(chunkSize)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize

		// Read chunk
		chunk := make([]byte, readSize)
		if _, err := file.ReadAt(chunk, pos); err != nil && err != io.EOF {
			return nil, err
		}

		// Prepend to buffer
		buffer = append(chunk, buffer...)

		// Split into lines
		scanner := bufio.NewScanner(bufio.NewReader(&byteReader{data: buffer}))
		var tempLines []string
		for scanner.Scan() {
			tempLines = append(tempLines, scanner.Text())
		}

		lines = tempLines
	}

	// Take last N lines and reverse
	start := 0
	if len(lines) > limit {
		start = len(lines) - limit
	}
	lines = lines[start:]

	// Reverse the lines so newest first
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	// Parse JSON lines
	var result []map[string]interface{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // Skip invalid JSON
		}
		result = append(result, entry)
	}

	return result, nil
}

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// mergeAndSortLogs merges logs from two files and sorts by time descending
func mergeAndSortLogs(backendLogs, dashengLogs []map[string]interface{}, err1, err2 error) []map[string]interface{} {
	var allLogs []map[string]interface{}
	if err1 == nil {
		allLogs = append(allLogs, backendLogs...)
	}
	if err2 == nil {
		allLogs = append(allLogs, dashengLogs...)
	}

	// Sort by time descending
	sort.Slice(allLogs, func(i, j int) bool {
		ti := getTimeField(allLogs[i])
		tj := getTimeField(allLogs[j])
		return ti.After(tj)
	})

	return allLogs
}

// getTimeField extracts time field from log entry
func getTimeField(entry map[string]interface{}) time.Time {
	// Try common time field names
	for _, field := range []string{"time", "timestamp", "@timestamp", "created_at"} {
		if t, ok := entry[field].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, t); err == nil {
				return parsed
			}
			if parsed, err := time.Parse("2006-01-02T15:04:05.000Z07:00", t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
