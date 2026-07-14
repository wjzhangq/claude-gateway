package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/db"
)

// AnalysisHandler serves the side-channel endpoints that cmd/check --analyze uses
// to pull the pending-analysis queue and write verdicts back (feature 004). These
// endpoints are authenticated with the session secret (Bearer), same as the
// ipgeo endpoints, so the analyzer never opens the SQLite file directly and the
// server stays the single writer.
type AnalysisHandler struct {
	db       *db.DB
	maxRetry int
}

func NewAnalysisHandler(database *db.DB, maxRetry int) *AnalysisHandler {
	if maxRetry <= 0 {
		maxRetry = 3
	}
	return &AnalysisHandler{db: database, maxRetry: maxRetry}
}

// SetMaxRetry updates the retry ceiling (used on config reload).
func (h *AnalysisHandler) SetMaxRetry(n int) {
	if n > 0 {
		h.maxRetry = n
	}
}

// GetPending godoc: GET /admin/api/analyze/pending?limit=500
// Returns not-yet-analyzed records whose retry_count is under the ceiling.
func (h *AnalysisHandler) GetPending(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	// order=recent returns the newest records first; anything else keeps the
	// default oldest-first queue order.
	newestFirst := c.Query("order") == "recent"
	records, err := h.db.ListPending(limit, h.maxRetry, newestFirst)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if records == nil {
		records = []*db.PendingRecord{}
	}
	c.JSON(http.StatusOK, gin.H{"records": records})
}

type writeResultsBody struct {
	Results []*db.AnalysisResult `json:"results"`
}

// PostResults godoc: POST /admin/api/analyze/results
// Applies each verdict in a single transaction: write back + delete, or retry.
func (h *AnalysisHandler) PostResults(c *gin.Context) {
	var body writeResultsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	counts, err := h.db.WriteBackResults(body.Results)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"updated": counts.Updated,
		"deleted": counts.Deleted,
		"retried": counts.Retried,
		"purged":  counts.Purged,
	})
}

// PurgeOldPending godoc: DELETE /admin/api/analyze/pending/stale?before_id=N
// Deletes all pending_analysis rows with id < before_id, evicting stored user
// intent signals that pre-date the current --recent batch.
func (h *AnalysisHandler) PurgeOldPending(c *gin.Context) {
	beforeID, err := strconv.ParseInt(c.Query("before_id"), 10, 64)
	if err != nil || beforeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "before_id must be a positive integer"})
		return
	}
	purged, err := h.db.DeletePendingBefore(beforeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"purged": purged})
}
