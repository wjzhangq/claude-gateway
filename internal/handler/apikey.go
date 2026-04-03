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

// ListKeys godoc: GET /api/keys  (shows backend channel keys only)
func (h *APIKeyHandler) ListKeys(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	keys, err := h.db.ListAPIKeysByUserAndChannel(userID, "backend")
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

	// Check user status before creating key
	user, err := h.db.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user info"})
		return
	}
	if user == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "user not found"})
		return
	}
	if user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user is not approved yet, please contact admin"})
		return
	}

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
	var quota int64
	if user != nil {
		quota = user.DailyQuotaTokens
	}
	h.keyStore.Add(keyStr, &auth.KeyInfo{
		KeyID:       k.ID,
		UserID:      userID,
		Itcode:      user.Itcode,
		DailyQuotaTokens: quota,
		UserStatus:  user.Status,
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

// SetAutoDowngrade godoc: PUT /api/keys/:id/auto-downgrade
func (h *APIKeyHandler) SetAutoDowngrade(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		AutoDowngrade bool `json:"auto_downgrade"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.UpdateAPIKeyAutoDowngrade(id, req.AutoDowngrade); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadKeys()
	c.JSON(http.StatusOK, gin.H{"auto_downgrade": req.AutoDowngrade})
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

// CheckKey godoc: POST /api/check_key (no auth required)
func (h *APIKeyHandler) CheckKey(c *gin.Context) {
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key is required"})
		return
	}

	// Look up key in memory key store
	info := h.keyStore.Get(req.Key)
	if info == nil {
		c.JSON(http.StatusOK, gin.H{"itcode": "", "created_at": ""})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"itcode":     info.Itcode,
		"created_at": info.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// RenameKey godoc: PUT /api/keys/:id/rename  (user renames own key)
// Also used by admin: PUT /admin/api/keys/:id/rename
func (h *APIKeyHandler) RenameKey(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.RenameAPIKey(id, req.Name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"name": req.Name})
}

// AdminListKeys godoc: GET /admin/api/keys
func (h *APIKeyHandler) AdminListKeys(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	keys, total, err := h.db.ListAllAPIKeys(userID, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys, "total": total, "page": page, "page_size": pageSize})
}

// SwitchChannel godoc: PUT /api/keys/:id/channel
// Body: {"channel": "aws"} or {"channel": "backend"}
func (h *APIKeyHandler) SwitchChannel(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Channel string `json:"channel" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Channel != "backend" && req.Channel != "aws" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel must be 'backend' or 'aws'"})
		return
	}

	// Switching to aws requires user to have aws_enabled
	if req.Channel == "aws" {
		userID := c.GetInt64(middleware.CtxUserID)
		user, err := h.db.GetUserByID(userID)
		if err != nil || user == nil || !user.AWSEnabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "AWS channel not enabled for this user"})
			return
		}
	}

	if err := h.db.UpdateAPIKeyChannel(id, req.Channel); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadKeys()
	c.JSON(http.StatusOK, gin.H{"channel": req.Channel})
}

// ListAWSKeys godoc: GET /api/aws/keys
func (h *APIKeyHandler) ListAWSKeys(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	keys, err := h.db.ListAPIKeysByUserAndChannel(userID, "aws")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

// CreateAWSKey godoc: POST /api/aws/keys
func (h *APIKeyHandler) CreateAWSKey(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	var req struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&req)

	user, err := h.db.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user info"})
		return
	}
	if user == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "user not found"})
		return
	}
	if user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user is not approved"})
		return
	}
	if !user.AWSEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "AWS channel not enabled for this user"})
		return
	}

	keyStr, err := auth.GenerateKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key generation failed"})
		return
	}

	k := &model.APIKey{
		UserID:  userID,
		Key:     keyStr,
		Name:    req.Name,
		Status:  "active",
		Channel: "aws",
	}
	if err := h.db.CreateAPIKey(k); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	h.keyStore.Add(keyStr, &auth.KeyInfo{
		KeyID:            k.ID,
		UserID:           userID,
		Itcode:           user.Itcode,
		DailyQuotaTokens: user.DailyQuotaTokens,
		UserStatus:       user.Status,
		Channel:          "aws",
	})

	c.JSON(http.StatusCreated, gin.H{"key": k})
}

// AdminListAWSKeys godoc: GET /admin/api/aws/keys
func (h *APIKeyHandler) AdminListAWSKeys(c *gin.Context) {
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	// Filter to channel='aws'
	keys, total, err := h.db.ListAllAPIKeysByChannel("aws", userID, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"keys": keys, "total": total, "page": page, "page_size": pageSize})
}

// TransferKey godoc: PUT /admin/api/keys/:id/transfer
func (h *APIKeyHandler) TransferKey(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Itcode string `json:"itcode" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := h.db.TransferAPIKey(id, req.Itcode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadKeys()
	c.JSON(http.StatusOK, gin.H{"user_id": user.ID, "itcode": user.Itcode})
}
