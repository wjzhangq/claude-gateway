package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/claude-gateway/claude-gateway/internal/db"
	"github.com/claude-gateway/claude-gateway/internal/middleware"
)

// StatsHandler serves usage statistics endpoints.
type StatsHandler struct {
	db *db.DB
}

func NewStatsHandler(database *db.DB) *StatsHandler {
	return &StatsHandler{db: database}
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
