package handler

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
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

// parseEmailLocal extracts and validates the local part (itcode) from an email address.
// Returns (itcode, error). Does not enforce any specific domain—caller decides what to do with it.
func parseEmailLocal(email string) (string, error) {
	email = strings.TrimSpace(email)
	if !regexp.MustCompile(`^[^@]+@[^@]+\.[^@]+$`).MatchString(email) {
		return "", fmt.Errorf("invalid email format")
	}
	parts := strings.Split(email, "@")
	itcode := strings.TrimSpace(parts[0])
	if itcode == "" {
		return "", fmt.Errorf("empty local part")
	}
	return itcode, nil
}

// canonicalEmail returns the authoritative email address for an itcode (itcode@configured_domain).
// This is the address where verification codes are sent, regardless of what domain the user typed.
func (h *AuthHandler) canonicalEmail(itcode string) string {
	return itcode + "@" + h.cfg.EmailDomain
}

// CheckEmail godoc: POST /api/auth/check-email
// Checks if an email (itcode) exists and whether a password is set.
// Used by login page on blur to provide early feedback and enable password reset flow.
// Note: This is an account enumeration endpoint—requested for UX but exposes existence.
func (h *AuthHandler) CheckEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}

	// Extract itcode from email
	itcode, err := parseEmailLocal(req.Email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
		return
	}

	// Look up user by itcode
	user, err := h.db.GetUserByItcode(itcode)
	if err != nil {
		logger.Errorf("check-email: db error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	if user == nil {
		c.JSON(http.StatusOK, gin.H{
			"exists":        false,
			"password_set":  false,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"exists":       true,
		"password_set": user.PasswordHash != "",
	})
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

	// Extract itcode from email (any domain accepted, we send to canonical domain)
	itcode, err := parseEmailLocal(req.Email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
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
	h.codeStore.Set(itcode, code) // Store by itcode (not email) for consistent lookup

	// Send to canonical email address (itcode@configured_domain), ignoring user's typed domain
	canonicalAddr := h.canonicalEmail(itcode)
	if h.emailService != nil {
		if err := h.emailService.SendVerificationCode(canonicalAddr, code); err != nil {
			if strings.Contains(err.Error(), "rate limit") {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			} else {
				logger.Warnf("send email to %s: %v", canonicalAddr, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send verification code"})
			}
			return
		}
		logger.Infof("password reset code sent to %s", canonicalAddr)
	} else {
		// Fallback: log code if email service not configured (dev mode)
		logger.Infof("verification code for %s: %s (email service not configured)", canonicalAddr, code)
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

	// Extract itcode from email
	itcode, err := parseEmailLocal(req.Email)
	if err != nil {
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

	// Extract itcode from email
	itcode, err := parseEmailLocal(req.Email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email format"})
		return
	}

	// Validate code (keyed by itcode, not email)
	if !h.codeStore.Verify(itcode, req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}

	// Validate password strength
	if err := auth.ValidatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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
