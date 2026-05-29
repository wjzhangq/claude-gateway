package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/db"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/perftest"
)

type BackendLister interface {
	GetBackendNames() []string
}

type PerfTestHandler struct {
	db      *db.DB
	runner  *perftest.Runner
	config  *config.Config
	backends BackendLister
	mu      sync.Mutex
	running map[int64]context.CancelFunc
	subs    map[int64][]chan perfEvent
	subsMu  sync.Mutex
}

type perfEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

func NewPerfTestHandler(database *db.DB, runner *perftest.Runner, cfg *config.Config, backends BackendLister) *PerfTestHandler {
	return &PerfTestHandler{
		db:       database,
		runner:   runner,
		config:   cfg,
		backends: backends,
		running:  make(map[int64]context.CancelFunc),
		subs:     make(map[int64][]chan perfEvent),
	}
}

type startRunRequest struct {
	Channels    []perftest.ChannelConfig `json:"channels"`
	InputSizes  []int                    `json:"input_sizes"`
	OutputSizes []int                    `json:"output_sizes"`
}

func (h *PerfTestHandler) StartRun(c *gin.Context) {
	if h.runner.IsRunning() {
		c.JSON(http.StatusConflict, gin.H{"error": "a performance test is already running"})
		return
	}

	var req startRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.Channels) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one channel is required"})
		return
	}
	if len(req.InputSizes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one input size is required"})
		return
	}
	if len(req.OutputSizes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one output size is required"})
		return
	}

	totalCells := len(req.Channels) * len(req.InputSizes) * len(req.OutputSizes)

	channelsJSON, _ := json.Marshal(req.Channels)
	inputsJSON, _ := json.Marshal(req.InputSizes)
	outputsJSON, _ := json.Marshal(req.OutputSizes)

	uid := c.GetInt64(middleware.CtxUserID)
	initiatedBy := fmt.Sprintf("user_%d", uid)

	runID, err := h.db.CreatePerfTestRun(initiatedBy, channelsJSON, inputsJSON, outputsJSON, totalCells)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create test run: " + err.Error()})
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.running[runID] = cancel
	h.mu.Unlock()

	h.runner.SetRunning(true)

	go h.executeRun(ctx, runID, req)

	c.JSON(http.StatusOK, gin.H{
		"id":          runID,
		"status":      "running",
		"total_cells": totalCells,
	})
}

func (h *PerfTestHandler) executeRun(ctx context.Context, runID int64, req startRunRequest) {
	defer func() {
		h.runner.SetRunning(false)
		h.mu.Lock()
		delete(h.running, runID)
		h.mu.Unlock()
		h.broadcast(runID, perfEvent{Type: "done", Data: gin.H{"run_id": runID}})
		h.cleanupSubs(runID)
	}()

	completed := 0
	total := len(req.Channels) * len(req.InputSizes) * len(req.OutputSizes)

	for _, ch := range req.Channels {
		for _, inputSize := range req.InputSizes {
			for _, outputSize := range req.OutputSizes {
				select {
				case <-ctx.Done():
					h.db.UpdatePerfTestRunStatus(runID, "cancelled", "")
					return
				default:
				}

				h.broadcast(runID, perfEvent{Type: "progress", Data: gin.H{
					"completed": completed,
					"total":     total,
					"channel":   ch.Name,
					"input":     inputSize,
					"output":    outputSize,
				}})

				cellResult := h.runner.RunCell(ctx, ch, inputSize, outputSize)

				dbResult := &db.PerfTestResult{
					RunID:              runID,
					Channel:            cellResult.Channel,
					Model:              cellResult.Model,
					InputTokens:        cellResult.InputTokens,
					MaxTokens:          cellResult.MaxTokens,
					TTFT_ms:            cellResult.TTFT_ms,
					TPOT_ms:            cellResult.TPOT_ms,
					TokensPerSecond:    cellResult.TokensPerSecond,
					ActualOutputTokens: cellResult.ActualOutputTokens,
					TotalDuration_ms:   cellResult.TotalDuration_ms,
					Status:             cellResult.Status,
					ErrorMsg:           cellResult.ErrorMsg,
				}
				h.db.InsertPerfTestResult(dbResult)
				h.db.IncrementPerfTestRunCompleted(runID)

				completed++

				h.broadcast(runID, perfEvent{Type: "result", Data: cellResult})
			}
		}
	}

	h.db.UpdatePerfTestRunStatus(runID, "completed", "")
}

func (h *PerfTestHandler) GetRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run id"})
		return
	}

	run, err := h.db.GetPerfTestRun(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}

	results, err := h.db.GetPerfTestResults(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"run":     run,
		"results": results,
	})
}

func (h *PerfTestHandler) ListRuns(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	runs, err := h.db.ListPerfTestRuns(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if runs == nil {
		runs = []db.PerfTestRun{}
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

func (h *PerfTestHandler) CancelRun(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run id"})
		return
	}

	h.mu.Lock()
	cancel, ok := h.running[id]
	h.mu.Unlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no running test with this id"})
		return
	}

	cancel()
	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

func (h *PerfTestHandler) StreamProgress(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid run id"})
		return
	}

	ch := make(chan perfEvent, 64)
	h.subsMu.Lock()
	h.subs[id] = append(h.subs[id], ch)
	h.subsMu.Unlock()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt.Data)
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", evt.Type, data)
			if canFlush {
				flusher.Flush()
			}
			if evt.Type == "done" {
				return
			}
		case <-time.After(30 * time.Second):
			fmt.Fprintf(c.Writer, ": keepalive\n\n")
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

func (h *PerfTestHandler) broadcast(runID int64, evt perfEvent) {
	h.subsMu.Lock()
	subs := h.subs[runID]
	h.subsMu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (h *PerfTestHandler) cleanupSubs(runID int64) {
	h.subsMu.Lock()
	subs := h.subs[runID]
	delete(h.subs, runID)
	h.subsMu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}

// GetOptions returns available channels, backends, and models for the perftest config form.
func (h *PerfTestHandler) GetOptions(c *gin.Context) {
	type channelOption struct {
		Name     string   `json:"name"`
		Label    string   `json:"label"`
		Models   []string `json:"models,omitempty"`
		Backends []string `json:"backends,omitempty"`
	}

	var channels []channelOption

	// Backend channel
	backendNames := h.backends.GetBackendNames()
	channels = append(channels, channelOption{
		Name:     "backend",
		Label:    "Backend (网梯)",
		Backends: backendNames,
	})

	// AWS channel
	if h.config.AWS.Region != "" {
		awsModels := make([]string, 0, len(h.config.AWS.ModelReplace))
		for k := range h.config.AWS.ModelReplace {
			awsModels = append(awsModels, k)
		}
		channels = append(channels, channelOption{
			Name:   "aws",
			Label:  "AWS Bedrock",
			Models: awsModels,
		})
	}

	// Public providers
	for _, p := range h.config.PublicProviders {
		if !p.Enabled {
			continue
		}
		channels = append(channels, channelOption{
			Name:   p.Name,
			Label:  p.Name,
			Models: p.Models,
		})
	}

	c.JSON(http.StatusOK, gin.H{"channels": channels})
}
