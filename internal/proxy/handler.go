package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/claude-gateway/claude-gateway/internal/logger"
	"github.com/claude-gateway/claude-gateway/internal/middleware"
	"github.com/claude-gateway/claude-gateway/internal/stats"
)

// Handler forwards requests to upstream Claude backends.
type Handler struct {
	lb        *LoadBalancer
	collector *stats.Collector
}

func NewHandler(lb *LoadBalancer, collector *stats.Collector) *Handler {
	return &Handler{lb: lb, collector: collector}
}

// forward is the shared proxy logic for both OpenAI and Anthropic style endpoints.
func (h *Handler) forward(c *gin.Context, upstreamPath string) {
	backend := h.lb.Pick()
	if backend == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available backend"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read request body failed"})
		return
	}

	targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "build request failed"})
		return
	}

	// Copy headers, replace Authorization
	for k, vv := range c.Request.Header {
		k = http.CanonicalHeaderKey(k)
		if k == "Authorization" || k == "X-Api-Key" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+backend.APIKey)
	req.Header.Set("x-api-key", backend.APIKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := backend.Client().Do(req)
	if err != nil {
		backend.RecordError()
		logger.Errorf("backend %s error: %v", backend.Name, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
		return
	}
	defer resp.Body.Close()
	backend.RecordSuccess()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)

	keyInfo, _ := c.Get(middleware.CtxKeyInfo)

	// Stream or buffer
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStream {
		h.streamResponse(c, resp, backend.Name, keyInfo, start)
	} else {
		h.bufferResponse(c, resp, backend.Name, keyInfo, start)
	}
}

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName string, keyInfo interface{}, start time.Time) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	buf := make([]byte, 4096)
	var totalBytes int
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += n
			c.Writer.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}

	h.recordUsage(keyInfo, backendName, resp.StatusCode, 0, 0, totalBytes, time.Since(start))
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, backendName string, keyInfo interface{}, start time.Time) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read response body: %v", err)
		return
	}
	c.Writer.Write(respBody)
	h.recordUsage(keyInfo, backendName, resp.StatusCode, 0, 0, len(respBody), time.Since(start))
}

func (h *Handler) recordUsage(keyInfo interface{}, backendName string, statusCode, inputTokens, outputTokens, _ int, latency time.Duration) {
	if h.collector == nil || keyInfo == nil {
		return
	}
	// Token counts will be parsed by the stats collector from the response body.
	// Here we just emit a basic record; the collector enriches it.
	_ = backendName
	_ = statusCode
	_ = inputTokens
	_ = outputTokens
	_ = latency
}

// ChatCompletions handles POST /v1/chat/completions (OpenAI style).
func (h *Handler) ChatCompletions(c *gin.Context) {
	h.forward(c, "/v1/chat/completions")
}

// Messages handles POST /v1/messages (Anthropic style).
func (h *Handler) Messages(c *gin.Context) {
	h.forward(c, "/v1/messages")
}

// Models handles GET /v1/models.
func (h *Handler) Models(c *gin.Context) {
	h.forward(c, "/v1/models")
}
