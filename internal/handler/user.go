package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// UserHandler manages user CRUD (admin only).
type UserHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
}

func NewUserHandler(database *db.DB, ks *auth.KeyStore) *UserHandler {
	return &UserHandler{db: database, keyStore: ks}
}

// ListUsers godoc: GET /admin/api/users
// Query params: page, page_size, search (itcode prefix), sort_by, sort_order
func (h *UserHandler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := c.Query("search")
	sortBy := c.DefaultQuery("sort_by", "id")
	sortOrder := c.DefaultQuery("sort_order", "desc")
	users, total, err := h.db.ListUsersWithStats(page, pageSize, search, sortBy, sortOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "page": page, "page_size": pageSize})
}

// SearchUsers godoc: GET /admin/api/users/search?q=prefix&limit=10
func (h *UserHandler) SearchUsers(c *gin.Context) {
	q := c.Query("q")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	results, err := h.db.SearchUsersByItcode(q, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": results})
}

// GetUser godoc: GET /admin/api/users/:id
func (h *UserHandler) GetUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	user, err := h.db.GetUserByID(id)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, user)
}

// CreateUser godoc: POST /admin/api/users
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req struct {
		Itcode           string  `json:"itcode" binding:"required"`
		Name             string  `json:"name"`
		Role             string  `json:"role"`
		Status           string  `json:"status"`
		GroupID          int     `json:"group_id"`
		DailyQuotaUSD    float64 `json:"daily_quota_usd"`
		AWSDailyQuotaUSD float64 `json:"aws_daily_quota_usd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Status == "" {
		req.Status = "pending"
	}
	user := &model.User{
		Itcode:           req.Itcode,
		Name:             req.Name,
		Role:             req.Role,
		GroupID:          req.GroupID,
		Status:           req.Status,
		DailyQuotaUSD:    req.DailyQuotaUSD,
		AWSDailyQuotaUSD: req.AWSDailyQuotaUSD,
	}
	if err := h.db.CreateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, user)
}

// UpdateUser godoc: PUT /admin/api/users/:id
func (h *UserHandler) UpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	user, err := h.db.GetUserByID(id)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	oldStatus := user.Status

	var req struct {
		Name             *string  `json:"name"`
		Status           *string  `json:"status"`
		Role             *string  `json:"role"`
		GroupID          *int     `json:"group_id"`
		DailyQuotaUSD    *float64 `json:"daily_quota_usd"`
		AWSDailyQuotaUSD *float64 `json:"aws_daily_quota_usd"`
		AWSEnabled       *bool    `json:"aws_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name != nil {
		user.Name = *req.Name
	}
	if req.Status != nil {
		user.Status = *req.Status
	}
	if req.Role != nil {
		user.Role = *req.Role
	}
	if req.GroupID != nil {
		user.GroupID = *req.GroupID
	}
	if req.DailyQuotaUSD != nil {
		user.DailyQuotaUSD = *req.DailyQuotaUSD
	}
	if req.AWSDailyQuotaUSD != nil {
		user.AWSDailyQuotaUSD = *req.AWSDailyQuotaUSD
	}
	if req.AWSEnabled != nil {
		user.AWSEnabled = *req.AWSEnabled
	}
	if err := h.db.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Sync KeyStore if status changed
	if req.Status != nil && *req.Status != oldStatus {
		h.keyStore.UpdateUserStatus(user.ID, user.Status)
	}

	c.JSON(http.StatusOK, user)
}

// UpdateItcode godoc: PUT /admin/api/users/:id/itcode
func (h *UserHandler) UpdateItcode(c *gin.Context) {
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
	if err := h.db.UpdateUserItcode(id, req.Itcode); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"itcode": req.Itcode})
}
