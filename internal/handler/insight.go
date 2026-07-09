package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/db"
)

// InsightHandler serves usage ranking, per-user insight, and org tagging endpoints.
// Migrated from the standalone sky-insight service into the gateway admin API.
type InsightHandler struct {
	db  *db.DB
	cfg *config.Config
}

func NewInsightHandler(database *db.DB, cfg *config.Config) *InsightHandler {
	return &InsightHandler{db: database, cfg: cfg}
}

// UpdateConfig replaces the config reference (used during reload).
func (h *InsightHandler) UpdateConfig(cfg *config.Config) {
	h.cfg = cfg
}

// GetRanking godoc: GET /admin/api/insight/ranking
// Query params: days (0=all, 7/30/90=last N days), limit (1-1000, default 500)
func (h *InsightHandler) GetRanking(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))

	ranking, err := h.db.GetUsageRanking(days, limit, h.cfg.RankingHiddenItcodes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	meta, err := h.db.GetRankingMeta()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ranking == nil {
		ranking = []*db.RankingUser{}
	}
	c.JSON(http.StatusOK, gin.H{
		"ranking":          ranking,
		"total_users":      len(ranking),
		"registered_total": meta.RegisteredTotal,
		"data_updated_at":  meta.DataUpdatedAt,
		"server_time":      time.Now().Format(time.RFC3339),
	})
}

// GetUserInsight godoc: GET /admin/api/insight/user/:id
func (h *InsightHandler) GetUserInsight(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	user, err := h.db.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	backendModels, err := h.db.GetModelDistribution("daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	awsModels, err := h.db.GetModelDistribution("aws_daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	backendTrend, err := h.db.GetDailyTrend("daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	awsTrend, err := h.db.GetDailyTrend("aws_daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	backendSummary, err := h.db.GetInsightSummary("daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	awsSummary, err := h.db.GetInsightSummary("aws_daily_stats", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	weekly, err := h.db.GetWeeklyTrend(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	peakDays, err := h.db.GetPeakDays(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":          user.ID,
			"itcode":      user.Itcode,
			"name":        user.Name,
			"status":      user.Status,
			"aws_enabled": user.AWSEnabled,
			"created_at":  user.CreatedAt,
		},
		"org_tag": gin.H{
			"department": user.Department,
			"role_tag":   user.RoleTag,
			"note":       user.OrgNote,
		},
		"summary": gin.H{
			"backend": backendSummary,
			"aws":     awsSummary,
		},
		"model_distribution": gin.H{
			"backend": backendModels,
			"aws":     awsModels,
		},
		"daily_trend": gin.H{
			"backend": backendTrend,
			"aws":     awsTrend,
		},
		"weekly_trend": weekly,
		"peak_days":    peakDays,
	})
}

// GetOrgList godoc: GET /admin/api/insight/org
func (h *InsightHandler) GetOrgList(c *gin.Context) {
	users, err := h.db.ListOrgUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if users == nil {
		users = []*db.OrgUser{}
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": len(users)})
}

type orgTagUpdateBody struct {
	Department string `json:"department"`
	RoleTag    string `json:"role_tag"`
	Note       string `json:"note"`
}

// UpdateOrgTag godoc: PUT /admin/api/insight/org/:id
func (h *InsightHandler) UpdateOrgTag(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	var body orgTagUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateUserOrgTag(userID, body.Department, body.RoleTag, body.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id": userID,
		"tag": gin.H{
			"department": body.Department,
			"role_tag":   body.RoleTag,
			"note":       body.Note,
		},
	})
}

type orgBatchBody struct {
	Updates []db.OrgTagUpdate `json:"updates"`
}

// BatchUpdateOrgTags godoc: POST /admin/api/insight/org/batch
func (h *InsightHandler) BatchUpdateOrgTags(c *gin.Context) {
	var body orgBatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	n, err := h.db.BatchUpdateOrgTags(body.Updates)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": n})
}
