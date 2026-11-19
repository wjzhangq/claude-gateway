package handler

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
)

// AuthHandler handles login/logout and password reset flows.
type AuthHandler struct {
	db           *db.DB
	codeStore    *auth.CodeStore
	sessionStore *auth.SessionStore
	emailService *auth.EmailService
	cfg          *config.AuthConfig
	rootCfg      *config.Config // full config for aws_channel_closed etc.
}

func NewAuthHandler(database *db.DB, cs *auth.CodeStore, ss *auth.SessionStore, cfg *config.AuthConfig, rootCfg *config.Config) *AuthHandler {
	var emailSvc *auth.EmailService
	if cfg.ResendAPIKey != "" {
		emailSvc = auth.NewEmailService(cfg.ResendAPIKey, cfg.ResendFromAddress, cfg.EmailRateLimitSec)
	}
	return &AuthHandler{
		db:           database,
		codeStore:    cs,
		sessionStore: ss,
		emailService: emailSvc,
		cfg:          cfg,
		rootCfg:      rootCfg,
	}
}

// SendCode godoc: POST /api/auth/send-code
// Repurposed for password reset: sends a verification code to the user's email.
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}

	// Validate email format and domain
	if !strings.Contains(req.Email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
		return
	}

	// Extract itcode from email (strip @domain)
	parts := strings.Split(req.Email, "@")
	itcode := parts[0]
	domain := parts[1]

	// Validate email domain
	if domain != h.cfg.EmailDomain {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("only @%s emails are accepted", h.cfg.EmailDomain)})
		return
	}

	// Look up user by itcode
	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		logger.Errorf("send-code: db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Generate 6-digit code
	n, _ := cryptorand.Int(cryptorand.Reader, big.NewInt(1000000))
	code := fmt.Sprintf("%06d", n.Int64())
	h.codeStore.Set(req.Email, code) // Store by email (not itcode) for reset flow

	// Send via Resend API
	if h.emailService != nil {
		if err := h.emailService.SendVerificationCode(req.Email, code); err != nil {
			if strings.Contains(err.Error(), "rate limit") {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			} else {
				logger.Warnf("send email to %s: %v", req.Email, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send verification code"})
			}
			return
		}
		logger.Infof("password reset code sent to %s", req.Email)
	} else {
		// Fallback: log code if email service not configured (dev mode)
		logger.Infof("verification code for %s: %s (email service not configured)", req.Email, code)
	}

	c.JSON(http.StatusOK, gin.H{"message": "code sent"})
}

// Login godoc: POST /api/auth/login
// Email + password login.
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Email      string `json:"email"      binding:"required"`
		Password   string `json:"password"   binding:"required"`
		RememberMe bool   `json:"remember_me"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and password are required"})
		return
	}

	// Validate email format
	if !strings.Contains(req.Email, "@") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Extract itcode from email
	parts := strings.Split(req.Email, "@")
	itcode := parts[0]
	domain := parts[1]

	// Validate email domain
	if domain != h.cfg.EmailDomain {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Look up user by itcode (GetUserByItcode now returns password_hash)
	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		logger.Errorf("login: db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Check if password is set
	if user.PasswordHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "password_not_set", "message": "Please use 'Forgot/Reset Password' to set your initial password"})
		return
	}

	// Compare password
	if err := auth.ComparePassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Check user status
	if user.Status != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user account is pending or disabled"})
		return
	}

	sess := sessions.Default(c)
	sess.Set("user_id", user.ID)
	sess.Set("user_role", user.Role)

	// Set session MaxAge: 7 days for remember_me, 1 day otherwise
	maxAge := 86400 // 1 day default
	if req.RememberMe {
		maxAge = 7 * 24 * 60 * 60 // 7 days
	}
	sess.Options(sessions.Options{
		MaxAge:   maxAge,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false, // production should set true (requires HTTPS)
	})
	if err := sess.Save(); err != nil {
		logger.Errorf("login: session save error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session error"})
		return
	}

	// Store session in SessionStore for server-side tracking (survives restarts)
	sessionID := sess.ID()
	h.sessionStore.Set(sessionID, user.ID, user.Role, time.Duration(maxAge)*time.Second)
	logger.Infof("user %s logged in, session_id=%s, maxAge=%ds", user.Itcode, sessionID, maxAge)

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":                 user.ID,
			"itcode":             user.Itcode,
			"email":              user.Email(h.cfg.EmailDomain),
			"role":               user.Role,
			"status":             user.Status,
			"aws_enabled":        user.AWSEnabled,
			"aws_channel_closed": h.awsChannelClosed(),
		},
	})
}

// ResetPassword godoc: POST /api/auth/reset-password
// Verifies the code and sets a new password.
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req struct {
		Email       string `json:"email"        binding:"required"`
		Code        string `json:"code"         binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email, code, and new_password are required"})
		return
	}

	// Validate code
	if !h.codeStore.Verify(req.Email, req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}

	// Validate password strength
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Extract itcode from email
	parts := strings.Split(req.Email, "@")
	if len(parts) != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
		return
	}
	itcode := parts[0]

	// Look up user
	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		logger.Errorf("reset-password: db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Hash new password
	passwordHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		logger.Errorf("reset-password: hash error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Update password in DB
	if err := h.db.UpdateUserPassword(user.ID, passwordHash); err != nil {
		logger.Errorf("reset-password: update error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	logger.Infof("password reset successful for user %s", user.Itcode)
	c.JSON(http.StatusOK, gin.H{"message": "password reset successfully"})
}

// Logout godoc: POST /api/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	sess := sessions.Default(c)
	sessionID := sess.ID()
	sess.Clear()
	_ = sess.Save()

	// Remove from SessionStore
	h.sessionStore.Delete(sessionID)
	logger.Debugf("user logged out, session_id=%s", sessionID)

	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// awsChannelClosed is a convenience accessor: it returns true when the root
// config declares aws.channel_closed. Safe to call even if rootCfg is nil.
func (h *AuthHandler) awsChannelClosed() bool {
	if h.rootCfg == nil {
		return false
	}
	return h.rootCfg.AWS.ChannelClosed
}

// Me godoc: GET /api/me — returns up-to-date current user info from DB.
func (h *AuthHandler) Me(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":                 user.ID,
			"itcode":             user.Itcode,
			"email":              user.Email(h.cfg.EmailDomain),
			"role":               user.Role,
			"status":             user.Status,
			"aws_enabled":        user.AWSEnabled,
			"aws_channel_closed": h.awsChannelClosed(),
		},
	})
}
