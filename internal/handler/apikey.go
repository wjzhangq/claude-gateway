package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// APIKeyHandler manages API key CRUD.
type APIKeyHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
}

func NewAPIKeyHandler(database *db.DB, ks *auth.KeyStore) *APIKeyHandler {
	return &APIKeyHandler{db: database, keyStore: ks}
}

// ListKeys godoc: GET /api/keys
func (h *APIKeyHandler) ListKeys(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	keys, err := h.db.ListAPIKeysByUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

// CreateKey godoc: POST /api/keys
func (h *APIKeyHandler) CreateKey(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	var req struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&req)

	keyStr, err := auth.GenerateKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key generation failed"})
		return
	}

	k := &model.APIKey{
		UserID: userID,
		Key:    keyStr,
		Name:   req.Name,
		Status: "active",
	}
	if err := h.db.CreateAPIKey(k); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Load user to get quota
	user, _ := h.db.GetUserByID(userID)
	var quota int64
	if user != nil {
		quota = user.QuotaTokens
	}
	h.keyStore.Add(keyStr, &auth.KeyInfo{
		KeyID:       k.ID,
		UserID:      userID,
		QuotaTokens: quota,
		UserStatus:  "active",
	})

	c.JSON(http.StatusCreated, gin.H{"key": k})
}

// DisableKey godoc: PUT /api/keys/:id/disable
func (h *APIKeyHandler) DisableKey(c *gin.Context) {
	h.setKeyStatus(c, "disabled")
}

// EnableKey godoc: PUT /api/keys/:id/enable
func (h *APIKeyHandler) EnableKey(c *gin.Context) {
	h.setKeyStatus(c, "active")
}

func (h *APIKeyHandler) setKeyStatus(c *gin.Context, status string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.db.UpdateAPIKeyStatus(id, status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Sync memory: fetch the key record to get the key string
	// For simplicity, reload all keys (low frequency operation)
	h.reloadKeys()
	c.JSON(http.StatusOK, gin.H{"status": status})
}

// DeleteKey godoc: DELETE /api/keys/:id
func (h *APIKeyHandler) DeleteKey(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.db.DeleteAPIKey(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadKeys()
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *APIKeyHandler) reloadKeys() {
	keys, err := h.db.ListAllActiveAPIKeys()
	if err != nil {
		return
	}
	users, err := h.db.ListUsers()
	if err != nil {
		return
	}
	userMap := make(map[int64]*model.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}
	apiKeys := make([]model.APIKey, len(keys))
	for i, k := range keys {
		apiKeys[i] = *k
	}
	h.keyStore.Load(apiKeys, userMap)
}
