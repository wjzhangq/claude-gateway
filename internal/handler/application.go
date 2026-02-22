package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/model"
)

// ApplicationHandler manages model access applications.
type ApplicationHandler struct {
	db *db.DB
}

func NewApplicationHandler(database *db.DB) *ApplicationHandler {
	return &ApplicationHandler{db: database}
}

// Submit godoc: POST /api/applications
func (h *ApplicationHandler) Submit(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	var req struct {
		Model  string `json:"model"  binding:"required"`
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	app := &model.Application{
		UserID: userID,
		Model:  req.Model,
		Reason: req.Reason,
	}
	if err := h.db.CreateApplication(app); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, app)
}

// ListMine godoc: GET /api/applications
func (h *ApplicationHandler) ListMine(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	apps, err := h.db.ListApplications(userID, c.Query("status"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"applications": apps})
}

// ListAll godoc: GET /admin/api/applications  (admin)
func (h *ApplicationHandler) ListAll(c *gin.Context) {
	apps, err := h.db.ListApplications(0, c.DefaultQuery("status", "pending"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"applications": apps})
}

// Review godoc: PUT /admin/api/applications/:id/review  (admin)
func (h *ApplicationHandler) Review(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"` // approved | rejected
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be approved or rejected"})
		return
	}

	reviewerID := c.GetInt64("session_user_id")
	if err := h.db.ReviewApplication(id, reviewerID, req.Status, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": req.Status})
}
