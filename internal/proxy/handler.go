package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/stats"
)

// Handler forwards requests to upstream Claude backends.
type Handler struct {
	lb                *LoadBalancer
	collector         *stats.Collector
	mu                sync.RWMutex
	modelReplacements map[string]string
}

func NewHandler(lb *LoadBalancer, collector *stats.Collector, modelReplacements map[string]string) *Handler {
	return &Handler{lb: lb, collector: collector, modelReplacements: modelReplacements}
}

// detectOpenClaw checks if the request is from OpenClaw.
// Criteria: User-Agent contains "OpenAI/JS" AND first 320 bytes of body
// contain "role", "system", "openclaw" (case-insensitive) in order.
func detectOpenClaw(userAgent string, body []byte) bool {
	if !strings.Contains(userAgent, "OpenAI/JS") {
		return false
	}
	snippet := body
	if len(snippet) > 320 {
		snippet = snippet[:320]
	}
	s := strings.ToLower(string(snippet))
	i1 := strings.Index(s, "role")
	if i1 < 0 {
		return false
	}
	i2 := strings.Index(s[i1:], "system")
	if i2 < 0 {
		return false
	}
	i3 := strings.Index(s[i1+i2:], "openclaw")
	return i3 >= 0
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

	// Detect OpenClaw request
	isOpenClaw := detectOpenClaw(c.GetHeader("User-Agent"), body)
	if isOpenClaw {
		c.Set("is_openclaw", true)
	}

	// Apply model replacements: if request model contains a configured pattern, replace it
	h.mu.RLock()
	replacements := h.modelReplacements
	h.mu.RUnlock()
	for pattern, replacement := range replacements {
		if strings.Contains(reqModel, pattern) {
			oldToken := `"model":"` + reqModel + `"`
			newToken := `"model":"` + replacement + `"`
			body = bytes.Replace(body, []byte(oldToken), []byte(newToken), 1)
			// also handle space after colon: "model": "value"
			oldTokenSpace := `"model": "` + reqModel + `"`
			newTokenSpace := `"model": "` + replacement + `"`
			body = bytes.Replace(body, []byte(oldTokenSpace), []byte(newTokenSpace), 1)
			reqModel = replacement
			break
		}
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
	backend.RecordStatusCode(resp.StatusCode)

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
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, resp.StatusCode, start, isOpenClaw)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, resp.StatusCode, start, isOpenClaw)
	}
}

const streamTailSize = 2048

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, statusCode int, start time.Time, isOpenClaw bool) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()
	// tail keeps only the last streamTailSize bytes for token parsing
	tail := make([]byte, 0, streamTailSize)
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			c.Writer.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
			// sliding window: keep only last streamTailSize bytes
			combined := append(tail, buf[:n]...)
			if len(combined) > streamTailSize {
				tail = combined[len(combined)-streamTailSize:]
			} else {
				tail = combined
			}
		}
		if err != nil {
			break
		}
	}

	in, out := parseStreamTokens(tail)
	h.emitUsage(keyInfo, backendName, model, statusCode, in, out, time.Since(start), isOpenClaw)
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, statusCode int, start time.Time, isOpenClaw bool) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read response body: %v", err)
		return
	}
	c.Writer.Write(respBody)

	in, out := parseBodyTokens(respBody)
	h.emitUsage(keyInfo, backendName, model, statusCode, in, out, time.Since(start), isOpenClaw)
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

// parseStreamTokens scans SSE lines for usage data, keeping the last seen token counts.
// Uses line-by-line scan to avoid allocating a large slice of all lines.
func parseStreamTokens(data []byte) (input, output int) {
	var in, out int
	for len(data) > 0 {
		// find next newline
		idx := bytes.IndexByte(data, '\n')
		var line []byte
		if idx < 0 {
			line = data
			data = nil
		} else {
			line = data[:idx]
			data = data[idx+1:]
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		i, o := parseBodyTokens(payload)
		if i > 0 || o > 0 {
			in, out = i, o
		}
	}
	return in, out
}

// costUSD estimates cost based on token counts and model.
// Pricing reference: https://www.anthropic.com/pricing & https://openai.com/api-pricing/
func costUSD(model string, inputTokens, outputTokens int) float64 {
	// Default fallback
	inputPrice := 3.0   // per 1M tokens
	outputPrice := 15.0 // per 1M tokens

	m := strings.ToLower(model)
	switch {
	// Claude Haiku
	case strings.Contains(m, "claude-haiku"):
		inputPrice, outputPrice = 1.0, 5.0
	// Claude Opus (4.5/4.6)
	case strings.Contains(m, "claude-opus"):
		inputPrice, outputPrice = 5.0, 25.0
	// Claude Sonnet (4.5/4.6)
	case strings.Contains(m, "claude-sonnet"):
		inputPrice, outputPrice = 3.0, 15.0
	// GPT-5
	case strings.Contains(m, "gpt-5.3-codex"):
		inputPrice, outputPrice = 1.75, 14.0
	case strings.Contains(m, "gpt-5.4"):
		inputPrice, outputPrice = 2.5, 15.0
	// GPT-4o
	case strings.Contains(m, "gpt-4o"):
		inputPrice, outputPrice = 2.5, 10.0
	// GPT-4
	case strings.Contains(m, "gpt-4"):
		inputPrice, outputPrice = 30.0, 60.0
	// GPT-3.5
	case strings.Contains(m, "gpt-3.5"):
		inputPrice, outputPrice = 0.5, 1.5
	}

	return (float64(inputTokens)*inputPrice + float64(outputTokens)*outputPrice) / 1_000_000
}

func (h *Handler) emitUsage(keyInfo interface{}, backendName, model string, statusCode, inputTokens, outputTokens int, latency time.Duration, isOpenClaw bool) {
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
		IsOpenClaw:   isOpenClaw,
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
	h.forward(c, "/v1"+c.Param("path"))
}

// UpdateModelReplacements hot-swaps the model replacement map.
func (h *Handler) UpdateModelReplacements(m map[string]string) {
	h.mu.Lock()
	h.modelReplacements = m
	h.mu.Unlock()
}
