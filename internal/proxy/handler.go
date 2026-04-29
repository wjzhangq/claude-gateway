package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/stats"
)

// Handler forwards requests to upstream Claude backends.
type Handler struct {
	lb                *LoadBalancer
	collector         *stats.Collector
	keyStore          *auth.KeyStore
	config           *config.Config
	mu                sync.RWMutex
	modelReplacements map[string]string
	fallbackClient    *http.Client
}

func NewHandler(lb *LoadBalancer, collector *stats.Collector, keyStore *auth.KeyStore, cfg *config.Config, modelReplacements map[string]string) *Handler {
	return &Handler{
		lb:                lb,
		collector:         collector,
		keyStore:          keyStore,
		config:           cfg,
		modelReplacements: modelReplacements,
		fallbackClient:    &http.Client{Timeout: 5 * time.Minute},
	}
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

// detectHermes checks if the request is from a Hermes client.
// Criteria: User-Agent contains "OpenAI/Python" AND first 320 bytes of body
// contain "hermes" (case-insensitive).
func detectHermes(userAgent string, body []byte) bool {
	if !strings.Contains(userAgent, "OpenAI/Python") {
		return false
	}
	snippet := body
	if len(snippet) > 320 {
		snippet = snippet[:320]
	}
	s := strings.ToLower(string(snippet))
	return strings.Contains(s, "hermes")
}

// isClaudeModel checks if the model name refers to a Claude model.
func isClaudeModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "claude") ||
		strings.Contains(m, "sonnet") ||
		strings.Contains(m, "opus") ||
		strings.Contains(m, "haiku")
}

// parseUA extracts the UA product name from User-Agent header.
// - Takes the part before "/" (if exists)
// - Truncates to max 12 characters
// - If isOpenClaw is true, returns "openclaw" directly
// - If isHermes is true, returns "hermesclaw" directly
// - Returns lowercase
func parseUA(userAgent string, isOpenClaw, isHermes bool) string {
	if isOpenClaw {
		return "openclaw"
	}
	if isHermes {
		return "hermesclaw"
	}
	if userAgent == "" {
		return ""
	}
	ua := userAgent
	if idx := strings.Index(ua, "/"); idx > 0 {
		ua = ua[:idx]
	}
	if len(ua) > 12 {
		ua = ua[:12]
	}
	return strings.ToLower(ua)
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
		// Only block OpenClaw clients when requesting Claude models
		if isClaudeModel(reqModel) {
			logger.Warnf("OpenClaw client blocked on backend channel (claude model): user_id=%v model=%s ua=%s",
				func() interface{} {
					if info, ok := c.Get(middleware.CtxKeyInfo); ok {
						if ki, ok := info.(*auth.KeyInfo); ok {
							return ki.UserID
						}
					}
					return "?"
				}(),
				reqModel, c.GetHeader("User-Agent"))
			c.JSON(http.StatusForbidden, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "forbidden",
					"message": "OpenClaw client is not allowed to use Claude models on backend channel",
				},
			})
			return
		}
		logger.Infof("OpenClaw client allowed on backend channel (non-claude model): model=%s", reqModel)
	}

	// Detect Hermes client (counts as lobster traffic, but no blocking)
	isHermes := !isOpenClaw && detectHermes(c.GetHeader("User-Agent"), body)
	if isHermes {
		c.Set("is_hermes", true)
		logger.Infof("Hermes client detected on backend channel: model=%s ua=%s", reqModel, c.GetHeader("User-Agent"))
	}

	// Get key info for auto-downgrade check
	keyInfo, _ := c.Get(middleware.CtxKeyInfo)
	var autoDowngrade bool
	var keyStr string
	if info, ok := keyInfo.(*auth.KeyInfo); ok {
		autoDowngrade = info.AutoDowngrade
		keyStr = c.GetHeader("Authorization")
		if strings.HasPrefix(keyStr, "Bearer ") {
			keyStr = strings.TrimPrefix(keyStr, "Bearer ")
		}
	}

	// Check if we're in the downgraded period (skip original model and go directly to GPT)
	inDowngradedPeriod := autoDowngrade && keyStr != "" && h.keyStore.IsDowngraded(keyStr)

	// Check per-user daily backend spending limit
	if info, ok := keyInfo.(*auth.KeyInfo); ok {
		h.mu.RLock()
		cfg := h.config
		h.mu.RUnlock()

		// Effective daily limit resolution (highest priority first):
		// 1. If the user's itcode appears in config.user_daily_limits with backend_daily_usd > 0,
		//    use that value directly — it overrides both the global cap and the DB quota.
		// 2. Otherwise fall back to min(global BackendDailyMax, per-user DailyQuotaUSD),
		//    treating 0 as "no limit" for each.
		effectiveLimit := 0.0
		if cfg != nil {
			if override := cfg.LookupUserDailyLimit(info.Itcode); override != nil && override.BackendDailyUSD > 0 {
				effectiveLimit = override.BackendDailyUSD
			} else {
				globalMax := cfg.BackendDailyMax
				userMax := info.DailyQuotaUSD
				switch {
				case globalMax > 0 && userMax > 0:
					if globalMax < userMax {
						effectiveLimit = globalMax
					} else {
						effectiveLimit = userMax
					}
				case globalMax > 0:
					effectiveLimit = globalMax
				case userMax > 0:
					effectiveLimit = userMax
				}
			}
		}

		if effectiveLimit > 0 {
			todayCost := h.keyStore.GetDailyCost(info.UserID)
			if todayCost >= effectiveLimit {
				logger.Warnf("daily backend limit exceeded: user_id=%d today=%.4f limit=%.4f", info.UserID, todayCost, effectiveLimit)
				c.JSON(http.StatusTooManyRequests, gin.H{
					"type": "error",
					"error": gin.H{
						"type":    "rate_limit_error",
						"message": "Daily backend spending limit exceeded. Please try again tomorrow.",
					},
				})
				return
			}
		}
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

	// Track if we attempted downgrade
	isDowngraded := false

	start := time.Now()
	var resp *http.Response

	// If in downgraded period, skip original model and go directly to GPT
	if inDowngradedPeriod {
		logger.Infof("key is in downgraded period, using GPT directly")
		var fbModel string
		resp, fbModel, err = h.doRequestWithDowngrade(c, backend, upstreamPath, body, reqModel, isOpenClaw, isHermes)
		if err == nil && resp != nil {
			isDowngraded = true
			reqModel = fbModel
		}
	} else {
		resp, err = h.doRequest(c, backend, upstreamPath, targetURL, body)
		if err != nil {
			backend.RecordError()
			logger.Errorf("backend %s error: %v", backend.Name, err)

			// Try downgrade if enabled and this is the first attempt
			if autoDowngrade {
				logger.Infof("attempting auto-downgrade for model %s, isOpenClaw=%v, isHermes=%v", reqModel, isOpenClaw, isHermes)
				var fbModel string
				resp, fbModel, err = h.doRequestWithDowngrade(c, backend, upstreamPath, body, reqModel, isOpenClaw, isHermes)
				if err == nil && resp != nil {
					isDowngraded = true
					reqModel = fbModel
				}
			}

			if err != nil || resp == nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
				return
			}
		}
	}

	defer resp.Body.Close()
	backend.RecordSuccess()
	backend.RecordStatusCode(resp.StatusCode)

	// Check if we need to retry with downgrade (response indicates failure)
	if autoDowngrade && !isDowngraded && resp.StatusCode >= 400 {
		resp.Body.Close()
		backend.RecordStatusCode(resp.StatusCode) // record the failure first

		logger.Infof("received error status %d, attempting auto-downgrade for model %s, isOpenClaw=%v, isHermes=%v", resp.StatusCode, reqModel, isOpenClaw, isHermes)
		respNew, fbModel, errNew := h.doRequestWithDowngrade(c, backend, upstreamPath, body, reqModel, isOpenClaw, isHermes)
		if errNew == nil && respNew != nil {
			resp = respNew
			isDowngraded = true
			reqModel = fbModel
		} else if errNew != nil {
			// Downgrade also failed, return original error
			c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
			return
		}
	}

	// If downgrade succeeded, set the downgraded period
	if isDowngraded && keyStr != "" && h.config != nil && h.config.DowngradedTTL > 0 {
		h.keyStore.SetDowngradedUntil(keyStr, time.Now().Add(h.config.DowngradedTTL))
	}

	// Expose backend name for the request logger
	c.Set("proxy_backend", backend.Name)

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Header(k, v)
		}
	}

	// Inject X-Gateway-* informational headers
	if info, ok := keyInfo.(*auth.KeyInfo); ok {
		c.Header("X-Gateway-Model", reqModel)
		c.Header("X-Gateway-Channel", info.Channel)
		quota := info.DailyQuotaUSD
		if info.Channel == "aws" {
			quota = info.AWSDailyQuotaUSD
		}
		c.Header("X-Gateway-Daily-Quota", strconv.FormatFloat(quota, 'f', 4, 64))
	}

	c.Status(resp.StatusCode)

	// Stream or buffer
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStream {
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, isDowngraded)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, isDowngraded)
	}
}

// doRequest performs the actual HTTP request to the backend
func (h *Handler) doRequest(c *gin.Context, backend *Backend, upstreamPath, targetURL string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
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

	return backend.Client().Do(req)
}

// doRequestWithDowngrade retries the request with a downgraded model.
// If a fallback model is configured in config and served by a public provider,
// the request is forwarded to that provider. Otherwise falls back to same backend
// with a hardcoded model swap.
// Returns the response, the actual fallback model name used, and any error.
func (h *Handler) doRequestWithDowngrade(c *gin.Context, backend *Backend, upstreamPath string, body []byte, originalModel string, isOpenClaw, isHermes bool) (*http.Response, string, error) {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()

	// Try config-based fallback to public provider
	if cfg != nil && cfg.Fallback != "" {
		fallbackModel := cfg.Fallback
		provider := cfg.LookupPublicProvider(fallbackModel)
		if provider != nil {
			newBody := h.replaceModelInBody(body, originalModel, fallbackModel)
			if newBody == nil {
				newBody = body // use original body if model replacement failed
			}

			// Determine target base URL based on request path (same logic as publicproxy)
			var baseURL string
			if strings.HasSuffix(upstreamPath, "/messages") {
				baseURL = provider.AnthropicURL
			} else {
				baseURL = provider.OpenAIURL
			}

			if baseURL != "" {
				targetURL := buildPublicTargetURL(baseURL, upstreamPath)
				logger.Infof("fallback to public provider %s model=%s url=%s", provider.Name, fallbackModel, targetURL)
				resp, err := h.doRequestToProvider(c, targetURL, newBody, provider.APIKey)
				return resp, fallbackModel, err
			}
		}
	}

	// Fallback to same backend with hardcoded model swap
	fallbackModel := "gpt-5.3-codex"
	if isOpenClaw || isHermes {
		fallbackModel = "gpt-5.4"
	}

	// Replace model in body
	newBody := h.replaceModelInBody(body, originalModel, fallbackModel)
	if newBody == nil {
		return nil, "", fmt.Errorf("failed to replace model in body")
	}

	targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
	resp, err := h.doRequest(c, backend, upstreamPath, targetURL, newBody)
	return resp, fallbackModel, err
}

// doRequestToProvider performs an HTTP request to a public provider with the given API key.
func (h *Handler) doRequestToProvider(c *gin.Context, targetURL string, body []byte, apiKey string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
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
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	return h.fallbackClient.Do(req)
}

// buildPublicTargetURL constructs the full upstream URL for a public provider.
// Avoids double /v1 prefix when baseURL already ends with /v1.
func buildPublicTargetURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// replaceModelInBody creates a new body with the model replaced
func (h *Handler) replaceModelInBody(body []byte, oldModel, newModel string) []byte {
	// Try both formats: "model":"value" and "model": "value"
	oldToken := `"model":"` + oldModel + `"`
	newToken := `"model":"` + newModel + `"`
	newBody := bytes.Replace(body, []byte(oldToken), []byte(newToken), 1)
	if len(newBody) == len(body) {
		// Try with space format
		oldTokenSpace := `"model": "` + oldModel + `"`
		newTokenSpace := `"model": "` + newModel + `"`
		newBody = bytes.Replace(body, []byte(oldTokenSpace), []byte(newTokenSpace), 1)
	}
	if len(newBody) == len(body) {
		return nil
	}
	return newBody
}

const streamTailSize = 2048

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, isOpenClaw, isHermes, isDowngraded bool) {
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
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, in, out, time.Since(start), isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"))
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, isOpenClaw, isHermes, isDowngraded bool) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read response body: %v", err)
		return
	}
	c.Writer.Write(respBody)

	in, out := parseBodyTokens(respBody)
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, in, out, time.Since(start), isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"))
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

func (h *Handler) emitUsage(keyInfo interface{}, keyStr, backendName, model string, statusCode, inputTokens, outputTokens int, latency time.Duration, isOpenClaw, isHermes, isDowngraded bool, userAgent string) {
	if h.collector == nil || keyInfo == nil {
		return
	}
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok {
		return
	}

	total := inputTokens + outputTokens
	cost := costUSD(model, inputTokens, outputTokens)
	ua := parseUA(userAgent, isOpenClaw, isHermes)
	// For DB is_openclaw field: both openclaw and hermes count as lobster traffic
	isLobster := isOpenClaw || isHermes
	h.collector.Emit(stats.Record{
		UserID:       info.UserID,
		GroupID:      info.GroupID,
		APIKeyID:     info.KeyID,
		KeyStr:       keyStr,
		Model:        model,
		Backend:      backendName,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  total,
		CostUSD:      cost,
		StatusCode:   statusCode,
		Latency:      latency,
		IsOpenClaw:   isLobster,
		IsDowngraded: isDowngraded,
		UA:           ua,
	})

	// Accumulate backend daily cost for per-user quota tracking
	if cost > 0 && statusCode < 400 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
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

// UpdateConfig updates the handler config.
func (h *Handler) UpdateConfig(cfg *config.Config) {
	h.mu.Lock()
	h.config = cfg
	h.mu.Unlock()
}
