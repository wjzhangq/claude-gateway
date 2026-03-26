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

// ApplicationHandler manages account activation applications.
type ApplicationHandler struct {
	db       *db.DB
	keyStore *auth.KeyStore
}

func NewApplicationHandler(database *db.DB, ks *auth.KeyStore) *ApplicationHandler {
	return &ApplicationHandler{db: database, keyStore: ks}
}

// Submit godoc: POST /api/applications
func (h *ApplicationHandler) Submit(c *gin.Context) {
	userID := c.GetInt64(middleware.CtxUserID)
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	app := &model.Application{
		UserID: userID,
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
		Status  string `json:"status" binding:"required"` // approved | rejected
		Note    string `json:"note"`
		GroupID *int   `json:"group_id"` // optional: set user's group when approving
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be approved or rejected"})
		return
	}

	// Get application to find user
	app, err := h.db.GetApplicationByID(id)
	if err != nil || app == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "application not found"})
		return
	}

	reviewerID := c.GetInt64("session_user_id")
	if err := h.db.ReviewApplication(id, reviewerID, req.Status, req.Note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Sync user status: approved → active, rejected → disabled
	user, err := h.db.GetUserByID(app.UserID)
	if err == nil && user != nil {
		if req.Status == "approved" {
			user.Status = "active"
			if req.GroupID != nil {
				user.GroupID = *req.GroupID
			}
		} else {
			user.Status = "disabled"
		}
		if err := h.db.UpdateUser(user); err == nil {
			h.keyStore.UpdateUserStatus(user.ID, user.Status)
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": req.Status})
}
