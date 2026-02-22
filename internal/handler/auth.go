package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// AuthHandler handles login/logout and verification code flows.
type AuthHandler struct {
	db        *db.DB
	codeStore *auth.CodeStore
	cfg       *config.AuthConfig
}

func NewAuthHandler(database *db.DB, cs *auth.CodeStore, cfg *config.AuthConfig) *AuthHandler {
	return &AuthHandler{db: database, codeStore: cs, cfg: cfg}
}

// SendCode godoc: POST /api/auth/send-code
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req struct {
		Itcode     string `json:"itcode"      binding:"required"`
		InviteCode string `json:"invite_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate invite code before sending
	if h.cfg.InviteCode != "" && req.InviteCode != h.cfg.InviteCode {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid invite code"})
		return
	}

	code := fmt.Sprintf("%06d", rand.Intn(1000000))
	h.codeStore.Set(req.Itcode, code)

	if h.cfg.SendCodeURL != "" {
		if err := sendEmailCode(h.cfg.SendCodeURL, req.Itcode, code); err != nil {
			logger.Warnf("send email code to %s: %v", req.Itcode, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send verification code"})
			return
		}
	} else {
		logger.Infof("verification code for %s: %s", req.Itcode, code)
	}

	c.JSON(http.StatusOK, gin.H{"message": "code sent"})
}

func sendEmailCode(sendCodeURL, itcode, code string) error {
	email := itcode
	if !strings.Contains(itcode, "@") {
		email = itcode + "@lenovo.com"
	}

	html := generateEmailTemplate(code)

	payload := map[string]string{
		"email": email,
		"html":  html,
	}
	jsonPayload, _ := json.Marshal(payload)

	resp, err := http.Post(sendCodeURL, "application/json", bytes.NewReader(jsonPayload))
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	logger.Infof("verification code sent to %s", email)
	return nil
}

func generateEmailTemplate(code string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:sans-serif;padding:24px;">
  <h2>Claude Gateway 验证码</h2>
  <p>您的验证码为：</p>
  <p style="font-size:32px;font-weight:bold;letter-spacing:8px;color:#4f46e5;">%s</p>
  <p style="color:#6b7280;font-size:14px;">验证码 5 分钟内有效，请勿泄露给他人。</p>
</body>
</html>`, code)
}

// Login godoc: POST /api/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Itcode     string `json:"itcode"      binding:"required"`
		Code       string `json:"code"        binding:"required"`
		InviteCode string `json:"invite_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate invite code
	if h.cfg.InviteCode != "" && req.InviteCode != h.cfg.InviteCode {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid invite code"})
		return
	}

	if !h.codeStore.Verify(req.Itcode, req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired code"})
		return
	}

	user, err := h.db.GetUserByItcode(req.Itcode)
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
		"user": gin.H{
			"id":     user.ID,
			"itcode": user.Itcode,
			"role":   user.Role,
		},
	})
}

// Logout godoc: POST /api/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Clear()
	_ = sess.Save()
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}
