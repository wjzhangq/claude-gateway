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

	stats, err := h.db.GetDailyStats(userID, start, end, model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// backendStatWithLimit augments a db.BackendStat with the configured daily cap
// and the used percentage. DailyLimit is 0 when unlimited; UsedPct is nil in that
// case so the client can render the neutral "unlimited" treatment (FR-008, FR-010).
type backendStatWithLimit struct {
	*db.BackendStat
	DailyLimit float64  `json:"daily_limit"`
	UsedPct    *float64 `json:"used_pct"`
}

// fleetBudgetSummary is the top-level aggregate over all backends for the day.
type fleetBudgetSummary struct {
	DailyLimitTotal float64 `json:"daily_limit_total"` // sum of positive caps only (FR-004)
	UsedTotal       float64 `json:"used_total"`        // sum of cost across all backends (FR-005)
	UsedPct         float64 `json:"used_pct"`          // used_total / limit_total * 100, 0 when no caps
	HasUnlimited    bool    `json:"has_unlimited"`     // true if any backend lacks a positive cap (FR-009)
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

	rows := make([]backendStatWithLimit, len(stats))
	var summary fleetBudgetSummary
	for i, s := range stats {
		limit := h.config.LookupBackendDailyLimit(s.Backend)
		row := backendStatWithLimit{BackendStat: s, DailyLimit: limit}
		if limit > 0 {
			pct := s.CostUSD / limit * 100
			row.UsedPct = &pct
		}
		summary.UsedTotal += s.CostUSD
		rows[i] = row
	}

	// Fleet limit total covers every enabled backend's configured cap, not just
	// those with usage today — otherwise idle backends would drop out of the total
	// and 总上限 would understate the real budget.
	for i := range h.config.Backends {
		b := &h.config.Backends[i]
		if !b.Enabled {
			continue
		}
		if limit := h.config.LookupBackendDailyLimit(b.Name); limit > 0 {
			summary.DailyLimitTotal += limit
		} else {
			summary.HasUnlimited = true
		}
	}
	if summary.DailyLimitTotal > 0 {
		summary.UsedPct = summary.UsedTotal / summary.DailyLimitTotal * 100
	}

	c.JSON(http.StatusOK, gin.H{"stats": rows, "summary": summary})
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

	// Effective backend daily limit (mirrors proxy enforcement priority):
	// 1. DB quota override (user_quota_overrides table, set by admin).
	// 2. YAML user_daily_limits entry for this itcode.
	// 3. min(global BackendDailyMax, per-user DailyQuotaUSD), 0 = unlimited.
	var effectiveBackendLimit float64
	if dbOverride, hasOverride := h.keyStore.GetQuotaOverride(userID); hasOverride {
		effectiveBackendLimit = dbOverride
	} else if override := h.config.LookupUserDailyLimit(user.Itcode); override != nil && override.BackendDailyUSD > 0 {
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

// GetMyQuota godoc: GET /api/quota — API-key-authenticated quota + today's usage summary.
// Returns: itcode, backend daily limit/used/remaining, per-backend breakdown,
// and (if AWS enabled) AWS daily/monthly limit/used/remaining.
func (h *StatsHandler) GetMyQuota(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	today := time.Now().Format("2006-01-02")

	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	// ── Backend daily limit (mirrors proxy enforcement priority) ────────────────
	// 1. DB quota override (user_quota_overrides table, set by admin).
	// 2. YAML user_daily_limits entry for this itcode.
	// 3. min(global BackendDailyMax, per-user DailyQuotaUSD), 0 = unlimited.
	var backendLimit float64
	if dbOverride, hasOverride := h.keyStore.GetQuotaOverride(userID); hasOverride {
		backendLimit = dbOverride
	} else if override := h.config.LookupUserDailyLimit(user.Itcode); override != nil && override.BackendDailyUSD > 0 {
		backendLimit = override.BackendDailyUSD
	} else {
		globalMax := h.config.BackendDailyMax
		userMax := user.DailyQuotaUSD
		switch {
		case globalMax > 0 && userMax > 0:
			if globalMax < userMax {
				backendLimit = globalMax
			} else {
				backendLimit = userMax
			}
		case globalMax > 0:
			backendLimit = globalMax
		case userMax > 0:
			backendLimit = userMax
		}
	}

	backendUsed := h.keyStore.GetDailyCost(userID)
	var backendRemaining float64
	if backendLimit > 0 {
		backendRemaining = backendLimit - backendUsed
		if backendRemaining < 0 {
			backendRemaining = 0
		}
	}

	// ── Per-backend breakdown ─────────────────────────────────────────────────
	backends, err := h.db.GetUserTodayBackendBreakdown(userID, today)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"itcode":                  user.Itcode,
		"date":                    today,
		"backend_daily_limit":     backendLimit,
		"backend_daily_used":      backendUsed,
		"backend_daily_remaining": backendRemaining,
		"backends":                backends,
	}

	// ── AWS (only if enabled) ─────────────────────────────────────────────────
	if user.AWSEnabled {
		awsUsed := h.keyStore.GetAWSDailyCost(userID)

		awsGlobalMax := h.config.AWS.AWSDailyMax
		awsUserMax := user.AWSDailyQuotaUSD
		var awsLimit float64
		switch {
		case awsGlobalMax > 0 && awsUserMax > 0:
			if awsGlobalMax < awsUserMax {
				awsLimit = awsGlobalMax
			} else {
				awsLimit = awsUserMax
			}
		case awsGlobalMax > 0:
			awsLimit = awsGlobalMax
		case awsUserMax > 0:
			awsLimit = awsUserMax
		}

		var awsRemaining float64
		if awsLimit > 0 {
			awsRemaining = awsLimit - awsUsed
			if awsRemaining < 0 {
				awsRemaining = 0
			}
		}

		resp["aws_enabled"] = true
		resp["aws_daily_limit"] = awsLimit
		resp["aws_daily_used"] = awsUsed
		resp["aws_daily_remaining"] = awsRemaining
	}

	c.JSON(http.StatusOK, resp)
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
		"id", "model", "backend",
		"input_tokens", "output_tokens", "total_tokens", "cost_usd",
		"status_code", "latency_ms", "is_openclaw", "is_downgraded", "ua", "error_reason", "created_at",
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
			l.Backend,
			strconv.Itoa(l.InputTokens),
			strconv.Itoa(l.OutputTokens),
			strconv.Itoa(l.TotalTokens),
			strconv.FormatFloat(l.CostUSD, 'f', 8, 64),
			strconv.Itoa(l.StatusCode),
			strconv.FormatInt(l.Latency, 10),
			isOC,
			isDG,
			l.UA,
			l.ErrorReason,
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

	result, err := h.db.GetUserDailyCostRanking(date, limit, h.config.RankingHiddenItcodes)
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
		"id", "user_id", "itcode", "api_key_id", "model", "backend",
		"input_tokens", "output_tokens", "total_tokens", "cost_usd",
		"status_code", "latency_ms", "is_openclaw", "is_downgraded", "ua", "error_reason", "created_at",
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
			l.Backend,
			strconv.Itoa(l.InputTokens),
			strconv.Itoa(l.OutputTokens),
			strconv.Itoa(l.TotalTokens),
			strconv.FormatFloat(l.CostUSD, 'f', 8, 64),
			strconv.Itoa(l.StatusCode),
			strconv.FormatInt(l.Latency, 10),
			isOC,
			isDG,
			l.UA,
			l.ErrorReason,
			l.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
}

// GetGroups godoc: GET /admin/api/groups
func (h *StatsHandler) GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"groups": h.config.Groups})
}

// UpdateConfig replaces the config reference (used during reload).
func (h *StatsHandler) UpdateConfig(cfg *config.Config) {
	h.config = cfg
}
