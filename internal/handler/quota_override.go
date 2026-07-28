package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

type QuotaOverrideHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
}

func NewQuotaOverrideHandler(database *db.DB, ks *auth.KeyStore) *QuotaOverrideHandler {
	return &QuotaOverrideHandler{db: database, keyStore: ks}
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

	// Sync in-memory cache — use same expiry semantics as LoadQuotaOverrides (expires_at inclusive)
	today := time.Now().Format("2006-01-02")
	if req.IsTemporary && expiresAt != nil && *expiresAt <= today {
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
