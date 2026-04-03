package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

const (
	CtxKeyInfo   = "key_info"
	CtxUserID    = "user_id"
	CtxUserRole  = "user_role"
)

// AuthMiddleware validates the Bearer API key from Authorization header.
// It uses the in-memory KeyStore for O(1) lookup.
func AuthMiddleware(ks *auth.KeyStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			// Also check x-api-key header (Anthropic style)
			raw = c.GetHeader("x-api-key")
			if raw == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing api key"})
				return
			}
		} else {
			raw = strings.TrimPrefix(raw, "Bearer ")
			raw = strings.TrimPrefix(raw, "bearer ")
		}

		// Store raw key for logging even if auth fails
		keyPrefix := raw
		if len(keyPrefix) > 8 {
			keyPrefix = keyPrefix[:8] + "..."
		}
		c.Set("raw_api_key", keyPrefix)

		info := ks.Get(raw)
		if info == nil {
			logger.Debugf("auth: key not found in store (total keys: %d, key prefix: %s)", ks.Count(), keyPrefix)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired api key"})
			return
		}
		if info.UserStatus != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "user is disabled"})
			return
		}

		c.Set(CtxKeyInfo, info)
		c.Set(CtxUserID, info.UserID)
		c.Set("channel", info.Channel)
		c.Next()
	}
}

// SessionAuthMiddleware validates admin session for management API.
func SessionAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, exists := c.Get("session_user_id")
		if !exists || userID == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not logged in"})
			return
		}
		c.Next()
	}
}

// AdminRequired ensures the session user has admin role.
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get(CtxUserRole)
		if role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin required"})
			return
		}
		c.Next()
	}
}

// AWSEnabledRequired checks that the session user has aws_enabled = true.
func AWSEnabledRequired(database *db.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64(CtxUserID)
		user, err := database.GetUserByID(userID)
		if err != nil || user == nil || !user.AWSEnabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "AWS channel not enabled for this user"})
			return
		}
		c.Next()
	}
}
