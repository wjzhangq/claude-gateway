package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
)

// StatsHandler serves usage statistics endpoints.
type StatsHandler struct {
	db       *db.DB
	config   *config.Config
	keyStore *auth.KeyStore
}

func NewStatsHandler(database *db.DB, cfg *config.Config, keyStore *auth.KeyStore) *StatsHandler {
	return &StatsHandler{db: database, config: cfg, keyStore: keyStore}
}

// GetUsage godoc: GET /admin/api/usage
// Query params: user_id, start_date (YYYY-MM-DD), end_date, model, backend, page, page_size
func (h *StatsHandler) GetUsage(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	backend := c.Query("backend")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListUsageLogs(userID, start, end, model, backend, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"logs":      logs,
	})
}

// GetDailyStats godoc: GET /admin/api/usage/daily
func (h *StatsHandler) GetDailyStats(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	backend := c.Query("backend")

	stats, err := h.db.GetDailyStatsByProvider(userID, start, end, model, backend)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// GetBackendStats godoc: GET /admin/api/backends/stats
func (h *StatsHandler) GetBackendStats(c *gin.Context) {
	start := c.Query("start_date")
	end := c.Query("end_date")

	stats, err := h.db.GetBackendStats(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// GetMyUsage godoc: GET /api/usage  (user's own stats, session or API key auth)
func (h *StatsHandler) GetMyUsage(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	backend := c.Query("backend")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListUsageLogs(userID, start, end, model, backend, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"logs":      logs,
	})
}

// GetMyDailyStats godoc: GET /api/usage/daily
func (h *StatsHandler) GetMyDailyStats(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")

	stats, err := h.db.GetDailyStats(userID, start, end, model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// GetMyDashboard godoc: GET /api/dashboard — returns quota and remaining balance for the current user.
func (h *StatsHandler) GetMyDashboard(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)

	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	// Effective backend daily limit:
	// 1. If user appears in config.user_daily_limits with backend_daily_usd > 0, use that value directly.
	// 2. Otherwise fall back to min(global BackendDailyMax, per-user DailyQuotaUSD), 0 = unlimited.
	var effectiveBackendLimit float64
	if override := h.config.LookupUserDailyLimit(user.Itcode); override != nil && override.BackendDailyUSD > 0 {
		effectiveBackendLimit = override.BackendDailyUSD
	} else {
		globalMax := h.config.BackendDailyMax
		userMax := user.DailyQuotaUSD
		switch {
		case globalMax > 0 && userMax > 0:
			if globalMax < userMax {
				effectiveBackendLimit = globalMax
			} else {
				effectiveBackendLimit = userMax
			}
		case globalMax > 0:
			effectiveBackendLimit = globalMax
		case userMax > 0:
			effectiveBackendLimit = userMax
		}
	}

	backendUsed := h.keyStore.GetDailyCost(userID)
	backendRemaining := 0.0
	if effectiveBackendLimit > 0 {
		backendRemaining = effectiveBackendLimit - backendUsed
		if backendRemaining < 0 {
			backendRemaining = 0
		}
	}

	// Effective AWS daily limit = min(global aws_daily_max, per-user AWSDailyQuotaUSD), 0 = unlimited
	awsGlobalMax := h.config.AWS.AWSDailyMax
	awsUserMax := user.AWSDailyQuotaUSD
	var effectiveAWSLimit float64
	switch {
	case awsGlobalMax > 0 && awsUserMax > 0:
		if awsGlobalMax < awsUserMax {
			effectiveAWSLimit = awsGlobalMax
		} else {
			effectiveAWSLimit = awsUserMax
		}
	case awsGlobalMax > 0:
		effectiveAWSLimit = awsGlobalMax
	case awsUserMax > 0:
		effectiveAWSLimit = awsUserMax
	}

	awsUsed := h.keyStore.GetAWSDailyCost(userID)
	awsRemaining := 0.0
	if effectiveAWSLimit > 0 {
		awsRemaining = effectiveAWSLimit - awsUsed
		if awsRemaining < 0 {
			awsRemaining = 0
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"backend_daily_limit":     effectiveBackendLimit,
		"backend_daily_used":      backendUsed,
		"backend_daily_remaining": backendRemaining,
		"aws_daily_limit":         effectiveAWSLimit,
		"aws_daily_used":          awsUsed,
		"aws_daily_remaining":     awsRemaining,
	})
}

// ExportMyUsage godoc: GET /api/usage/export
// Query params: date (YYYY-MM-DD), model, backend
// Returns a CSV of the current user's usage logs for the given day.
func (h *StatsHandler) ExportMyUsage(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	date := c.Query("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	model := c.Query("model")
	backend := c.Query("backend")

	logs, err := h.db.ListUsageLogsAll(userID, date, date, model, backend)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("usage_%s.csv", date)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename="+filename)

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{
		"id", "model", "provider",
		"input_tokens", "output_tokens", "total_tokens", "cost_usd",
		"status_code", "latency_ms", "is_openclaw", "is_downgraded", "ua", "created_at",
	})
	for _, l := range logs {
		isOC := "0"
		if l.IsOpenClaw {
			isOC = "1"
		}
		isDG := "0"
		if l.IsDowngraded {
			isDG = "1"
		}
		_ = w.Write([]string{
			strconv.FormatInt(l.ID, 10),
			l.Model,
			l.Provider,
			strconv.Itoa(l.InputTokens),
			strconv.Itoa(l.OutputTokens),
			strconv.Itoa(l.TotalTokens),
			strconv.FormatFloat(l.CostUSD, 'f', 8, 64),
			strconv.Itoa(l.StatusCode),
			strconv.FormatInt(l.Latency, 10),
			isOC,
			isDG,
			l.UA,
			l.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

// GetGroupStats godoc: GET /admin/api/groups/stats
func (h *StatsHandler) GetGroupStats(c *gin.Context) {
	start := c.Query("start_date")
	end := c.Query("end_date")

	stats, err := h.db.GetGroupStats(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Populate group names from config
	groupMap := make(map[int]string)
	for _, g := range h.config.Groups {
		groupMap[g.ID] = g.Name
	}
	for _, s := range stats {
		if name, ok := groupMap[s.GroupID]; ok {
			s.GroupName = name
		} else {
			s.GroupName = "unknown"
		}
	}

	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// GetUserDailyCostRanking godoc: GET /admin/api/usage/user-daily
func (h *StatsHandler) GetUserDailyCostRanking(c *gin.Context) {
	date := c.Query("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	result, err := h.db.GetUserDailyCostRanking(date, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ExportUsage godoc: GET /admin/api/usage/export
// Query params: user_id, date (YYYY-MM-DD), model, backend
// Returns a CSV file with all usage logs for the given day.
func (h *StatsHandler) ExportUsage(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	date := c.Query("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	model := c.Query("model")
	backend := c.Query("backend")

	logs, err := h.db.ListUsageLogsAll(userID, date, date, model, backend)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	filename := fmt.Sprintf("usage_%s.csv", date)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename="+filename)

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{
		"id", "user_id", "itcode", "api_key_id", "model", "provider",
		"input_tokens", "output_tokens", "total_tokens", "cost_usd",
		"status_code", "latency_ms", "is_openclaw", "is_downgraded", "ua", "created_at",
	})
	for _, l := range logs {
		isOC := "0"
		if l.IsOpenClaw {
			isOC = "1"
		}
		isDG := "0"
		if l.IsDowngraded {
			isDG = "1"
		}
		_ = w.Write([]string{
			strconv.FormatInt(l.ID, 10),
			strconv.FormatInt(l.UserID, 10),
			l.Itcode,
			strconv.FormatInt(l.APIKeyID, 10),
			l.Model,
			l.Provider,
			strconv.Itoa(l.InputTokens),
			strconv.Itoa(l.OutputTokens),
			strconv.Itoa(l.TotalTokens),
			strconv.FormatFloat(l.CostUSD, 'f', 8, 64),
			strconv.Itoa(l.StatusCode),
			strconv.FormatInt(l.Latency, 10),
			isOC,
			isDG,
			l.UA,
			l.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

// GetGroups godoc: GET /admin/api/groups
func (h *StatsHandler) GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"groups": h.config.Groups})
}

// GetProviderStats godoc: GET /admin/api/provider/stats?provider=backend&period=today
func (h *StatsHandler) GetProviderStats(c *gin.Context) {
	provider := c.Query("provider")
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}
	period := c.DefaultQuery("period", "today")

	requests, costUSD, err := h.db.GetProviderStats(provider, period)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider": provider,
		"period":   period,
		"requests": requests,
		"cost_usd": costUSD,
	})
}

// GetProviderModelStats godoc: GET /admin/api/provider/model-stats?provider=backend&date=2026-01-01
func (h *StatsHandler) GetProviderModelStats(c *gin.Context) {
	provider := c.Query("provider")
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))

	stats, err := h.db.GetProviderModelStats(provider, date)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider": provider,
		"date":     date,
		"stats":    stats,
	})
}

func (h *StatsHandler) GetProviderDailyStats(c *gin.Context) {
	provider := c.Query("provider")
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	if startDate == "" || endDate == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start_date and end_date are required"})
		return
	}

	stats, err := h.db.GetProviderDailyStats(provider, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":   provider,
		"start_date": startDate,
		"end_date":   endDate,
		"stats":      stats,
	})
}

// GetUsageSummary godoc: GET /admin/api/usage/summary?date=2026-01-01&user_id=123&backend=aws
func (h *StatsHandler) GetUsageSummary(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))
	backend := c.Query("backend")

	summary, err := h.db.GetUsageDaySummaryByProvider(userID, date, backend)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetAdminOverview godoc: GET /admin/api/overview
func (h *StatsHandler) GetAdminOverview(c *gin.Context) {
	overview, err := h.db.GetAdminOverview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, overview)
}

// UpdateConfig replaces the config reference (used during reload).
func (h *StatsHandler) UpdateConfig(cfg *config.Config) {
	h.config = cfg
}
