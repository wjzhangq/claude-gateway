package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
)

// AWSStatsHandler serves AWS usage statistics endpoints.
type AWSStatsHandler struct {
	db       *db.DB
	config   *config.Config
	keyStore *auth.KeyStore
}

// NewAWSStatsHandler creates an AWSStatsHandler.
func NewAWSStatsHandler(database *db.DB, cfg *config.Config, ks *auth.KeyStore) *AWSStatsHandler {
	return &AWSStatsHandler{db: database, config: cfg, keyStore: ks}
}

// UpdateConfig replaces the config reference (used during reload).
func (h *AWSStatsHandler) UpdateConfig(cfg *config.Config) {
	h.config = cfg
}

// ─── User-side AWS endpoints ───────────────────────────────────────────────

// GetMyDashboard godoc: GET /api/aws/dashboard
func (h *AWSStatsHandler) GetMyDashboard(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	today := time.Now().Format("2006-01-02")
	monthStart := time.Now().Format("2006-01") + "-01"

	// Today's stats
	todayStats, err := h.db.GetAWSDailyStats(userID, today, today, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// This month's stats
	monthStats, err := h.db.GetAWSDailyStats(userID, monthStart, today, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var todayRequests int
	var todayCost float64
	for _, s := range todayStats {
		todayRequests += s.Requests
		todayCost += s.CostUSD
	}

	var monthRequests int
	var monthCost float64
	for _, s := range monthStats {
		monthRequests += s.Requests
		monthCost += s.CostUSD
	}

	// Resolve monthly limit for this user
	var awsMonthlyLimit float64
	cfg := h.config
	if cfg != nil {
		var itcode string
		if raw, ok := c.Get(middleware.CtxKeyInfo); ok {
			if ki, ok := raw.(*auth.KeyInfo); ok {
				itcode = ki.Itcode
			}
		}
		if override := cfg.LookupUserDailyLimit(itcode); override != nil && override.AWSMonthlyUSD > 0 {
			awsMonthlyLimit = override.AWSMonthlyUSD
		} else if cfg.AWS.AWSMonthlyMax > 0 {
			awsMonthlyLimit = cfg.AWS.AWSMonthlyMax
		}
	}

	resp := gin.H{
		"today_requests":    todayRequests,
		"today_cost_usd":    todayCost,
		"month_requests":    monthRequests,
		"month_cost_usd":    monthCost,
		"today_stats":       todayStats,
		"aws_monthly_limit": awsMonthlyLimit,
	}
	if awsMonthlyLimit > 0 {
		remaining := awsMonthlyLimit - monthCost
		if remaining < 0 {
			remaining = 0
		}
		resp["aws_monthly_remaining"] = remaining
	}
	c.JSON(http.StatusOK, resp)
}

// GetMyUsage godoc: GET /api/aws/usage
func (h *AWSStatsHandler) GetMyUsage(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListAWSUsageLogs(userID, start, end, model, page, pageSize)
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

// GetMyDailyStats godoc: GET /api/aws/usage/daily
func (h *AWSStatsHandler) GetMyDailyStats(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")

	stats, err := h.db.GetAWSDailyStats(userID, start, end, model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// ─── Admin AWS endpoints ───────────────────────────────────────────────────

// ListAWSUsers godoc: GET /admin/api/aws/users
func (h *AWSStatsHandler) ListAWSUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	users, total, err := h.db.ListAWSUsersWithStats(page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "page": page, "page_size": pageSize})
}

// ToggleAWSEnabled godoc: PUT /admin/api/aws/users/:id/toggle
func (h *AWSStatsHandler) ToggleAWSEnabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	user, err := h.db.GetUserByID(id)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	user.AWSEnabled = !user.AWSEnabled
	if err := h.db.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"aws_enabled": user.AWSEnabled})
}

// EnableAWSByItcode godoc: POST /admin/api/aws/users/enable
// Body: {"itcode": "xxx"} — finds user by itcode and sets aws_enabled=true.
func (h *AWSStatsHandler) EnableAWSByItcode(c *gin.Context) {
	var req struct {
		Itcode string `json:"itcode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "itcode is required"})
		return
	}
	user, err := h.db.GetUserByItcode(req.Itcode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在: " + req.Itcode})
		return
	}
	if user.AWSEnabled {
		c.JSON(http.StatusOK, gin.H{"aws_enabled": true, "message": "用户已开启 AWS"})
		return
	}
	user.AWSEnabled = true
	if err := h.db.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"aws_enabled": true, "user_id": user.ID, "itcode": user.Itcode})
}

// GetUsage godoc: GET /admin/api/aws/usage
func (h *AWSStatsHandler) GetUsage(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListAWSUsageLogs(userID, start, end, model, page, pageSize)
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

// GetDailyStats godoc: GET /admin/api/aws/usage/daily
func (h *AWSStatsHandler) GetDailyStats(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")

	stats, err := h.db.GetAWSDailyStats(userID, start, end, model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// GetUserDailyCostRanking godoc: GET /admin/api/aws/usage/user-daily
func (h *AWSStatsHandler) GetUserDailyCostRanking(c *gin.Context) {
	date := c.Query("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	result, err := h.db.GetAWSUserDailyCostRanking(date, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetBedrockStats godoc: GET /admin/api/aws/bedrock/stats
func (h *AWSStatsHandler) GetBedrockStats(c *gin.Context) {
	start := c.Query("start_date")
	end := c.Query("end_date")

	stats, err := h.db.GetAWSBedrockStats(start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}
