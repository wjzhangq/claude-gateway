package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/stats"
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

	// Extract model name from request body for usage tracking
	var reqModel string
	var reqJSON struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &reqJSON) == nil {
		reqModel = reqJSON.Model
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

	// Expose backend name for the request logger
	c.Set("proxy_backend", backend.Name)

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
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, resp.StatusCode, start)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, resp.StatusCode, start)
	}
}

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, statusCode int, start time.Time) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	var accumulated []byte
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			accumulated = append(accumulated, buf[:n]...)
			c.Writer.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}

	in, out := parseStreamTokens(accumulated)
	h.emitUsage(keyInfo, backendName, model, statusCode, in, out, time.Since(start))
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, statusCode int, start time.Time) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read response body: %v", err)
		return
	}
	c.Writer.Write(respBody)

	in, out := parseBodyTokens(respBody)
	h.emitUsage(keyInfo, backendName, model, statusCode, in, out, time.Since(start))
}

// parseBodyTokens extracts token counts from a non-streaming JSON response.
// Handles both OpenAI and Anthropic response formats.
func parseBodyTokens(body []byte) (input, output int) {
	var r struct {
		Usage struct {
			// OpenAI format
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			// Anthropic format
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, 0
	}
	input = r.Usage.PromptTokens + r.Usage.InputTokens
	output = r.Usage.CompletionTokens + r.Usage.OutputTokens
	return
}

// parseStreamTokens scans SSE lines for usage data in the final chunk.
func parseStreamTokens(data []byte) (input, output int) {
	// Look for usage in SSE data lines
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		in, out := parseBodyTokens(payload)
		if in > 0 || out > 0 {
			input = in
			output = out
		}
	}
	return
}

// costUSD estimates cost based on token counts and model.
// Uses approximate pricing; adjust as needed.
func costUSD(model string, inputTokens, outputTokens int) float64 {
	// Default to claude-3-5-sonnet pricing as fallback
	inputPrice := 3.0   // per 1M tokens
	outputPrice := 15.0 // per 1M tokens

	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude-opus-4") || strings.Contains(m, "opus-4"):
		inputPrice, outputPrice = 15.0, 75.0
	case strings.Contains(m, "claude-sonnet-4") || strings.Contains(m, "sonnet-4"):
		inputPrice, outputPrice = 3.0, 15.0
	case strings.Contains(m, "claude-haiku-3-5") || strings.Contains(m, "haiku-3-5"):
		inputPrice, outputPrice = 0.8, 4.0
	case strings.Contains(m, "claude-haiku"):
		inputPrice, outputPrice = 0.25, 1.25
	case strings.Contains(m, "claude-opus"):
		inputPrice, outputPrice = 15.0, 75.0
	case strings.Contains(m, "claude-sonnet"):
		inputPrice, outputPrice = 3.0, 15.0
	case strings.Contains(m, "gpt-4o"):
		inputPrice, outputPrice = 2.5, 10.0
	case strings.Contains(m, "gpt-4"):
		inputPrice, outputPrice = 30.0, 60.0
	case strings.Contains(m, "gpt-3.5"):
		inputPrice, outputPrice = 0.5, 1.5
	}

	return (float64(inputTokens)*inputPrice + float64(outputTokens)*outputPrice) / 1_000_000
}

func (h *Handler) emitUsage(keyInfo interface{}, backendName, model string, statusCode, inputTokens, outputTokens int, latency time.Duration) {
	if h.collector == nil || keyInfo == nil {
		return
	}
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok {
		return
	}

	total := inputTokens + outputTokens
	h.collector.Emit(stats.Record{
		UserID:       info.UserID,
		APIKeyID:     info.KeyID,
		Model:        model,
		Backend:      backendName,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  total,
		CostUSD:      costUSD(model, inputTokens, outputTokens),
		StatusCode:   statusCode,
		Latency:      latency,
	})
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

// Passthrough forwards any other /v1/* path to the upstream backend.
func (h *Handler) Passthrough(c *gin.Context) {
	h.forward(c, "/v1/"+c.Param("path"))
}
