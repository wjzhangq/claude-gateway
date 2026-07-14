package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/classify"
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
// Returns all users with their org/attribution tags plus the configured 负责人
// (attribution leader) list, so the org tab can render the leader dropdown.
func (h *InsightHandler) GetOrgList(c *gin.Context) {
	users, err := h.db.ListOrgUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if users == nil {
		users = []*db.OrgUser{}
	}
	leaders := h.cfg.AttributionLeaders
	if leaders == nil {
		leaders = []config.AttributionLeader{}
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": len(users), "leaders": leaders})
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

type orgLeaderUpdateBody struct {
	Leader string `json:"leader"` // attr_group; empty clears attribution
}

// UpdateOrgLeader godoc: PUT /admin/api/insight/org/:id/leader
// Sets the user's 负责人 (attr_group) and derives the team (attr_side) from the
// configured attribution_leaders list, so the user rolls up in the Token 归口 report.
func (h *InsightHandler) UpdateOrgLeader(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	var body orgLeaderUpdateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Empty leader clears attribution; otherwise the leader must be one of the
	// configured options, and its side is taken from config (not client input).
	var group, side string
	if body.Leader != "" {
		leader := h.cfg.LookupAttributionLeader(body.Leader)
		if leader == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知的负责人"})
			return
		}
		group, side = leader.Name, leader.Side
	}

	if err := h.db.UpdateUserLeader(userID, group, side); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user_id":    userID,
		"attr_group": group,
		"attr_side":  side,
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

// GetAttribution godoc: GET /admin/api/insight/attribution?days=0
// Query params: days (0=all, 7/30/90=last N days).
// Derives ASDC&SWS&NBC / SMB token attribution from the DB org columns
// (attr_side / attr_group / is_departed) merged with live daily_stats + aws_daily_stats.
func (h *InsightHandler) GetAttribution(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "0"))

	shenLabel, nonLabel := h.attributionLabels()
	attr, err := h.db.GetAttribution(days, h.cfg.RankingHiddenItcodes, shenLabel, nonLabel)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"meta": gin.H{
			"days":   attr.Days,
			"period": attr.Period,
			"labels": gin.H{"shen": shenLabel, "non": nonLabel},
		},
		"shen":            attr.Shen,
		"non":             attr.Non,
		"unmatched":       attr.Unmatched,
		"unmatched_total": attr.UnmatchedTotal,
		"departed":        attr.Departed,
		"departed_total":  attr.DepartedTotal,
		"departed_tokens": attr.DepartedTokens,
		"departed_cost":   attr.DepartedCost,
		"server_time":     time.Now().Format(time.RFC3339),
	})
}

// attributionLabels returns the display names for the two attribution sides,
// falling back to the canonical defaults when not configured.
func (h *InsightHandler) attributionLabels() (shen, non string) {
	shen, non = "ASDC&SWS&NBC", "SMB"
	if h.cfg != nil {
		if v := h.cfg.AttributionLabels["shen"]; v != "" {
			shen = v
		}
		if v := h.cfg.AttributionLabels["non"]; v != "" {
			non = v
		}
	}
	return shen, non
}

// GetAbuseInsight godoc: GET /admin/api/insight/abuse?window=day|week|month
// Aggregates tagged usage per person over the window, scores each, and returns the
// per-person portraits plus a review queue (score ≥ threshold). Identification
// only — the response carries no punitive action (FR-021).
func (h *InsightHandler) GetAbuseInsight(c *gin.Context) {
	windowStart := abuseWindowStart(c.DefaultQuery("window", "day"))

	recs, err := h.db.ListTaggedRecords(0, windowStart)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	clsCfg := classify.FromAnalyzeConfig(h.cfg.Analyze)

	// Group by user, aggregate, score.
	byUser := map[int64][]classify.Record{}
	for _, r := range recs {
		byUser[r.UserID] = append(byUser[r.UserID], r)
	}
	rollups := make([]classify.Rollup, 0, len(byUser))
	reviewQueue := make([]classify.Rollup, 0)
	for _, urecs := range byUser {
		ru := classify.Aggregate(urecs, windowStart, clsCfg)
		rollups = append(rollups, ru)
		if classify.NeedsReview(ru, clsCfg.Score) {
			reviewQueue = append(reviewQueue, ru)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"window_start": windowStart.Format(time.RFC3339),
		"users":        rollups,
		"review_queue": reviewQueue,
		"threshold":    clsCfg.Score.Threshold,
	})
}

// abuseWindowStart maps a window keyword to its start instant (gateway local time).
func abuseWindowStart(window string) time.Time {
	now := time.Now()
	switch window {
	case "week":
		return now.AddDate(0, 0, -7)
	case "month":
		return now.AddDate(0, -1, 0)
	default: // "day"
		return now.AddDate(0, 0, -1)
	}
}
