package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/responses"
	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// responsesUpstreamPath is the Anthropic Messages path the Responses adapter
// targets upstream: a Responses request is converted to an Anthropic Messages
// body and forwarded to the backend's /v1/messages endpoint.
const responsesUpstreamPath = "/v1/messages"

// HandleResponses serves POST /v1/responses (OpenAI Responses API, used by the
// Codex CLI) on the backend channel. It converts the request to Anthropic
// Messages, runs the shared web-search agent loop (which is a single plain round
// when no web_search tool is requested), and renders the assembled result back
// as a Responses object — JSON (non-stream) or a synthesized Responses SSE
// stream. The request body is passed in already-read by the router.
func (h *Handler) HandleResponses(c *gin.Context, body []byte) {
	req, err := responses.DecodeRequest(body)
	if err != nil {
		respondResponsesError(c, http.StatusBadRequest, "invalid_request_error", "invalid request body")
		return
	}

	anthReq, hasWebSearch, err := responses.ToMessagesRequest(req)
	if err != nil {
		if errors.Is(err, responses.ErrPreviousResponseID) {
			respondResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		respondResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	reqModel := anthReq.Model
	keyInfo, _ := c.Get(middleware.CtxKeyInfo)
	keyStr := extractKeyStr(c)

	// Apply the key's locked model, mirroring the backend passthrough path.
	if info, ok := keyInfo.(*auth.KeyInfo); ok && info.LockedModel != "" {
		reqModel = info.LockedModel
		anthReq.Model = info.LockedModel
	}

	backend := h.lb.Pick()
	if backend == nil {
		respondResponsesError(c, http.StatusServiceUnavailable, "api_error", "no available backend")
		return
	}

	h.mu.RLock()
	client := h.searxng
	cfg := h.webSearchCfg
	wsEnabled := cfg.Enabled && client != nil
	h.mu.RUnlock()

	clientWantsStream := req.Stream

	// Only rewrite tools when web_search was requested AND the feature is
	// enabled: RewriteTools drops the server tool and injects the client
	// web_search tool the loop resolves. Without web_search, function tools stay
	// as plain Anthropic tools and the loop runs a single round.
	if hasWebSearch {
		if wsEnabled {
			anthReq.RewriteTools()
		} else {
			// Feature off: strip the injected server tool so the upstream relay
			// (which can't execute it) never sees it.
			anthReq.Tools = stripWebSearchServerTool(anthReq.Tools)
			logger.Infof("responses: web_search requested but feature disabled; stripped server tool model=%s", reqModel)
		}
	}
	anthReq.Stream = false

	start := time.Now()

	// SSE setup: open the stream immediately so keep-alives during searches hold
	// the connection.
	var sse *responses.SSEWriter
	var pinger func()
	if clientWantsStream {
		flusher, _ := c.Writer.(http.Flusher)
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
		var flush func()
		if flusher != nil {
			flush = flusher.Flush
		}
		sse = responses.NewSSEWriter(c.Writer, flush, reqModel)
		pinger = sse.Ping
	}

	res, lerr := h.runWebSearchLoop(c, backend, responsesUpstreamPath, reqModel, anthReq, client, cfg, start, pinger)
	if lerr != nil {
		if lerr.Canceled {
			return
		}
		h.responsesLoopError(c, sse, lerr)
		return
	}

	h.emitUsage(keyInfo, keyStr, backend.Name, reqModel, http.StatusOK,
		res.Usage.InputTokens, res.Usage.OutputTokens, time.Since(start),
		false, false, false, c.Request.Header.Get("User-Agent"), c.ClientIP(), body)

	if sse != nil {
		sse.WriteResponse(res.Merged, res.Usage, res.StopReason)
	} else {
		resp := responses.ToResponse(res.Model, res.Merged, res.Usage, res.StopReason)
		c.Header("Content-Type", "application/json")
		c.JSON(http.StatusOK, resp)
	}
}

// responsesLoopError renders a loop failure to a Responses client. A first-round
// upstream error is mapped to a generic api_error (the upstream body is Anthropic
// -shaped and not meaningful to a Responses client).
func (h *Handler) responsesLoopError(c *gin.Context, sse *responses.SSEWriter, lerr *loopError) {
	msg := lerr.Message
	if msg == "" {
		msg = "upstream request failed"
	}
	logger.Errorf("responses: loop error: %s (status=%d)", msg, lerr.Status)
	if sse != nil {
		sse.WriteError("api_error", msg)
		return
	}
	status := http.StatusBadGateway
	respondResponsesError(c, status, "api_error", msg)
}

// respondResponsesError writes a Responses-shaped error object with the given
// HTTP status.
func respondResponsesError(c *gin.Context, status int, errType, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": msg}})
}

// extractKeyStr pulls the bearer/x-api-key value used for usage attribution,
// matching the backend passthrough logic.
func extractKeyStr(c *gin.Context) string {
	keyStr := c.GetHeader("Authorization")
	if strings.HasPrefix(keyStr, "Bearer ") {
		return strings.TrimPrefix(keyStr, "Bearer ")
	}
	if keyStr != "" {
		return keyStr
	}
	return c.GetHeader("x-api-key")
}

// stripWebSearchServerTool removes any Anthropic web_search server tool from the
// tools list, used when the injected server tool must not reach an upstream that
// can't execute it.
func stripWebSearchServerTool(tools []json.RawMessage) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(tools))
	for _, raw := range tools {
		var t struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &t); err == nil && strings.HasPrefix(t.Type, websearch.ServerToolPrefix) {
			continue
		}
		kept = append(kept, raw)
	}
	return kept
}
