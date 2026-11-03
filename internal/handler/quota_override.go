package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/model"
	"github.com/wjzhangq/claude-gateway/internal/quota"
)

type QuotaOverrideHandler struct {
	db       *db.DB
	config   *config.Config
	keyStore *auth.KeyStore
}

func NewQuotaOverrideHandler(database *db.DB, cfg *config.Config, ks *auth.KeyStore) *QuotaOverrideHandler {
	return &QuotaOverrideHandler{db: database, config: cfg, keyStore: ks}
}

func (h *QuotaOverrideHandler) List(c *gin.Context) {
	overrides, err := h.db.ListUserQuotaOverrides()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if overrides == nil {
		overrides = []*model.UserQuotaOverride{}
	}
	c.JSON(http.StatusOK, gin.H{"overrides": overrides})
}

type upsertQuotaOverrideRequest struct {
	QuotaUSD    float64 `json:"quota_usd"    binding:"min=0"`
	IsTemporary bool    `json:"is_temporary"`
	ExpiresAt   string  `json:"expires_at"`
	Note        string  `json:"note"`
}

func (h *QuotaOverrideHandler) Upsert(c *gin.Context) {
	itcode := c.Param("itcode")
	var req upsertQuotaOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var expiresAt *string
	if req.IsTemporary {
		if req.ExpiresAt == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at required for temporary quota"})
			return
		}
		if _, err := time.Parse("2006-01-02", req.ExpiresAt); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be YYYY-MM-DD"})
			return
		}
		expiresAt = &req.ExpiresAt
	}

	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if err := h.db.UpsertUserQuotaOverride(user.ID, req.QuotaUSD, req.IsTemporary, expiresAt, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Sync in-memory cache — same expiry semantics as LoadQuotaOverrides and the
	// admin page: the expiry date itself is still a valid day.
	today := time.Now().Format("2006-01-02")
	if req.IsTemporary && expiresAt != nil && *expiresAt < today {
		h.keyStore.DeleteQuotaOverride(user.ID)
	} else {
		h.keyStore.SetQuotaOverride(user.ID, req.QuotaUSD)
	}

	o, _ := h.db.GetUserQuotaOverride(user.ID)
	c.JSON(http.StatusOK, gin.H{"override": o})
}

func (h *QuotaOverrideHandler) Delete(c *gin.Context) {
	itcode := c.Param("itcode")

	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if err := h.db.DeleteUserQuotaOverride(user.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.keyStore.DeleteQuotaOverride(user.ID)
	c.Status(http.StatusNoContent)
}

// ── Quota status (read-only) ──────────────────────────────────────────────────

const (
	statusMaxItcodes       = 50
	statusDefaultLimit     = 20
	statusMaxLimit         = 200
	statusDefaultThreshold = 0.8
)

// quotaStatusUser reports a user's effective backend daily limit next to the
// spend already counted against it.
type quotaStatusUser struct {
	UserID         int64                    `json:"user_id"`
	Itcode         string                   `json:"itcode"`
	Name           string                   `json:"name"`
	DailyLimit     float64                  `json:"daily_limit"`
	DailyUsed      float64                  `json:"daily_used"`
	DailyRemaining float64                  `json:"daily_remaining"`
	UsedRatio      float64                  `json:"used_ratio"`
	LimitSource    string                   `json:"limit_source"`
	Override       *model.UserQuotaOverride `json:"override"`
}

// Status handles GET /admin/api/db/quota/status.
//
// Query params:
//   - itcode:    comma-separated itcodes (max 50). Unknown itcodes are skipped.
//   - over:      "true" keeps only users whose used/limit ratio >= threshold.
//   - threshold: ratio for `over`, default 0.8.
//   - limit:     max users returned when no itcode is given (default 20, max 200).
//
// Limits resolve through quota.ResolveBackendDaily and spend through the
// in-process counter, so the reported numbers are the ones the proxy enforces.
func (h *QuotaOverrideHandler) Status(c *gin.Context) {
	date, costs := h.keyStore.SnapshotDailyCosts()
	globalMax := h.config.BackendDailyMax

	overOnly := c.Query("over") == "true"
	threshold := statusDefaultThreshold
	if raw := c.Query("threshold"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			threshold = v
		}
	}

	users, err := h.statusTargets(c, costs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	overrides, err := h.db.ListUserQuotaOverrides()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	overrideByUser := make(map[int64]*model.UserQuotaOverride, len(overrides))
	for _, o := range overrides {
		overrideByUser[o.UserID] = o
	}

	limit := parseIntParam(c, "limit", statusDefaultLimit)
	if limit > statusMaxLimit {
		limit = statusMaxLimit
	}
	if limit < 1 {
		limit = statusDefaultLimit
	}

	result := make([]quotaStatusUser, 0, len(users))
	for _, u := range users {
		row := buildQuotaStatus(u, costs[u.ID], overrideByUser[u.ID],
			quota.ResolveBackendDaily(h.keyStore, u.ID, globalMax), globalMax)
		if overOnly && (row.DailyLimit <= 0 || row.UsedRatio < threshold) {
			continue
		}
		result = append(result, row)
	}

	// Highest pressure first — the users an operator needs to see.
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UsedRatio > result[j].UsedRatio
	})
	if len(result) > limit {
		result = result[:limit]
	}

	c.JSON(http.StatusOK, gin.H{
		"date":                     date,
		"global_backend_daily_max": globalMax,
		"users":                    result,
	})
}

// statusTargets resolves which users to report: the requested itcodes, or every
// user that has spent something today when no itcode is given.
func (h *QuotaOverrideHandler) statusTargets(c *gin.Context, costs map[int64]float64) ([]*model.User, error) {
	if raw := strings.TrimSpace(c.Query("itcode")); raw != "" {
		codes := make([]string, 0, statusMaxItcodes)
		for _, part := range strings.Split(raw, ",") {
			if code := strings.TrimSpace(part); code != "" {
				codes = append(codes, code)
			}
		}
		if len(codes) > statusMaxItcodes {
			return nil, fmt.Errorf("too many itcodes: %d (max %d)", len(codes), statusMaxItcodes)
		}
		users := make([]*model.User, 0, len(codes))
		for _, code := range codes {
			u, err := h.db.GetUserByItcode(code)
			if err != nil {
				return nil, err
			}
			if u != nil {
				users = append(users, u)
			}
		}
		return users, nil
	}

	// No itcode filter: report the users with spend today, which is what the
	// in-process counter can speak to.
	users := make([]*model.User, 0, len(costs))
	for userID := range costs {
		u, err := h.db.GetUserByID(userID)
		if err != nil {
			return nil, err
		}
		if u != nil {
			users = append(users, u)
		}
	}
	return users, nil
}

func buildQuotaStatus(u *model.User, used float64, override *model.UserQuotaOverride, limit, globalMax float64) quotaStatusUser {
	row := quotaStatusUser{
		UserID:      u.ID,
		Itcode:      u.Itcode,
		Name:        u.Name,
		DailyLimit:  limit,
		DailyUsed:   used,
		LimitSource: "unlimited",
		Override:    override,
	}
	switch {
	case limit <= 0:
		// unlimited: no remaining balance and no ratio to report
	case override != nil && !override.IsExpired && override.QuotaUSD == limit:
		row.LimitSource = "override"
	case limit == globalMax:
		row.LimitSource = "global"
	default:
		row.LimitSource = "override"
	}
	if limit > 0 {
		row.UsedRatio = used / limit
		if remaining := limit - used; remaining > 0 {
			row.DailyRemaining = remaining
		}
	}
	return row
}
