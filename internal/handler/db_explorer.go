package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
)

// DBExplorerHandler serves database schema and query endpoints.
type DBExplorerHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
}

func NewDBExplorerHandler(database *db.DB, keyStore *auth.KeyStore) *DBExplorerHandler {
	return &DBExplorerHandler{db: database, keyStore: keyStore}
}

// AdminAPIKeyAuth middleware validates that the request carries an admin API key.
func (h *DBExplorerHandler) AdminAPIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			raw = c.GetHeader("x-api-key")
			if raw == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing api key"})
				return
			}
		} else {
			raw = strings.TrimPrefix(raw, "Bearer ")
			raw = strings.TrimPrefix(raw, "bearer ")
		}

		info := h.keyStore.Get(raw)
		if info == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
			return
		}
		if info.UserStatus != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "user is disabled"})
			return
		}

		// Check user is admin
		user, err := h.db.GetUserByID(info.UserID)
		if err != nil || user == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}
		if user.Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin required"})
			return
		}

		c.Next()
	}
}

// GetSchema handles GET /admin/api/db/schema
func (h *DBExplorerHandler) GetSchema(c *gin.Context) {
	schema, err := h.db.GetSchema()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tables": schema})
}

// ExecuteQuery handles POST /admin/api/db/query
func (h *DBExplorerHandler) ExecuteQuery(c *gin.Context) {
	var req struct {
		SQL string `json:"sql" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sql field is required"})
		return
	}

	if strings.TrimSpace(req.SQL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sql cannot be empty"})
		return
	}

	result, err := h.db.ExecuteReadQuery(req.SQL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
