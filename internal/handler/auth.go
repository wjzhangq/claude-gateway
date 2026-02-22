package handler

import (
	"fmt"
	"math/rand"
	"net/http"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"

	"github.com/claude-gateway/claude-gateway/internal/auth"
	"github.com/claude-gateway/claude-gateway/internal/db"
	"github.com/claude-gateway/claude-gateway/internal/logger"
)

// AuthHandler handles login/logout and verification code flows.
type AuthHandler struct {
	db        *db.DB
	codeStore *auth.CodeStore
}

func NewAuthHandler(database *db.DB, cs *auth.CodeStore) *AuthHandler {
	return &AuthHandler{db: database, codeStore: cs}
}

// SendCode godoc: POST /api/auth/send-code
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	code := fmt.Sprintf("%06d", rand.Intn(1000000))
	h.codeStore.Set(req.Phone, code)

	// In production, call an SMS gateway here.
	// For development, log the code.
	logger.Infof("verification code for %s: %s", req.Phone, code)

	c.JSON(http.StatusOK, gin.H{"message": "code sent"})
}

// Login godoc: POST /api/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
		Code  string `json:"code"  binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !h.codeStore.Verify(req.Phone, req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}

	user, err := h.db.GetUserByPhone(req.Phone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "user not found"})
		return
	}
	if user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user is disabled"})
		return
	}

	sess := sessions.Default(c)
	sess.Set("user_id", user.ID)
	sess.Set("user_role", user.Role)
	if err := sess.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id": user.ID,
		"role":    user.Role,
		"name":    user.Name,
	})
}

// Logout godoc: POST /api/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Clear()
	_ = sess.Save()
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}
