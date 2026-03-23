package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
)

// StatsHandler serves usage statistics endpoints.
type StatsHandler struct {
	db     *db.DB
	config *config.Config
}

func NewStatsHandler(database *db.DB, cfg *config.Config) *StatsHandler {
	return &StatsHandler{db: database, config: cfg}
}

// GetUsage godoc: GET /admin/api/usage
// Query params: user_id, start_date (YYYY-MM-DD), end_date, model, page, page_size
func (h *StatsHandler) GetUsage(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	start := c.Query("start_date")
	end := c.Query("end_date")
	model := c.Query("model")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListUsageLogs(userID, start, end, model, page, pageSize)
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
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	logs, total, err := h.db.ListUsageLogs(userID, start, end, model, page, pageSize)
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

// GetGroups godoc: GET /admin/api/groups
func (h *StatsHandler) GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"groups": h.config.Groups})
}

// UpdateConfig replaces the config reference (used during reload).
func (h *StatsHandler) UpdateConfig(cfg *config.Config) {
	h.config = cfg
}
