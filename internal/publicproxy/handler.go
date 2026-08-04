package publicproxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/ipgeo"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/quota"
	"github.com/wjzhangq/claude-gateway/internal/stats"
	"github.com/wjzhangq/claude-gateway/internal/tokenest"
	"github.com/wjzhangq/claude-gateway/internal/usage"
)

// Handler transparently forwards requests to public third-party providers
// (e.g. Kimi, MiniMax) based on the requested model name.
type Handler struct {
	collector *stats.Collector
	keyStore  *auth.KeyStore
	mu        sync.RWMutex
	cfg       *config.Config
	client    *http.Client
	ipGeo     *ipgeo.Store
}

// NewHandler creates a new public proxy handler.
func NewHandler(collector *stats.Collector, keyStore *auth.KeyStore, cfg *config.Config) *Handler {
	return &Handler{
		collector: collector,
		keyStore:  keyStore,
		cfg:       cfg,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// SetIPGeo attaches an IP → city/HQ store used to tag usage records. Nil-safe:
// when unset, usage records carry empty IP/city and is_hq=false.
func (h *Handler) SetIPGeo(s *ipgeo.Store) { h.ipGeo = s }

// UpdateConfig hot-swaps the config (used on SIGHUP reload).
func (h *Handler) UpdateConfig(cfg *config.Config) {
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
}

func (h *Handler) getCfg() *config.Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// MatchModel checks if the model is served by a public provider and the user is allowed.
// Returns the provider if found and access granted, nil otherwise.
func (h *Handler) MatchModel(model, itcode string) *config.PublicProvider {
	cfg := h.getCfg()
	if cfg == nil {
		return nil
	}
	return cfg.LookupPublicProvider(model, itcode)
}

// IsPublicModel reports whether model belongs to any enabled public provider,
// ignoring user restrictions.
func (h *Handler) IsPublicModel(model string) bool {
	cfg := h.getCfg()
	if cfg == nil {
		return false
	}
	return cfg.IsPublicModel(model)
}

// Forward transparently proxies the request to the appropriate public provider.
// path is the original upstream path, e.g. "/v1/chat/completions" or "/v1/messages".
func (h *Handler) Forward(c *gin.Context, path string, body []byte, model string, provider *config.PublicProvider) {
	// Determine target base URL based on request path
	var baseURL string
	if strings.HasSuffix(path, "/messages") {
		baseURL = provider.AnthropicURL
	} else {
		baseURL = provider.OpenAIURL
	}

	if baseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "public provider " + provider.Name + " does not support this API format",
		})
		return
	}

	// Build target URL: baseURL + path suffix
	// e.g. "https://api.moonshot.cn/v1" + "/chat/completions"
	targetURL := buildTargetURL(baseURL, path)

	start := time.Now()

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create request failed"})
		return
	}

	// Copy headers from original request, replace auth
	for k, vv := range c.Request.Header {
		k = http.CanonicalHeaderKey(k)
		if k == "Authorization" || k == "X-Api-Key" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("x-api-key", provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		logger.Errorf("public provider %s error: %v", provider.Name, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "public provider request failed"})
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Header(k, v)
		}
	}

	// Inject informational headers
	keyInfo, _ := c.Get(middleware.CtxKeyInfo)
	keyStr := c.GetHeader("Authorization")
	keyStr = strings.TrimPrefix(keyStr, "Bearer ")
	keyStr = strings.TrimPrefix(keyStr, "bearer ")
	if keyStr == "" {
		keyStr = c.GetHeader("x-api-key")
	}

	c.Header("X-Gateway-Model", model)
	c.Header("X-Gateway-Provider", provider.Name)

	c.Status(resp.StatusCode)

	// Stream or buffer
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStream {
		h.streamResponse(c, resp, provider, model, keyInfo, keyStr, resp.StatusCode, start, body)
	} else {
		h.bufferResponse(c, resp, provider, model, keyInfo, keyStr, resp.StatusCode, start, body)
	}
}

// Models returns the list of public models as OpenAI-compatible model objects.
func (h *Handler) Models(itcode string) []gin.H {
	cfg := h.getCfg()
	if cfg == nil {
		return nil
	}

	now := time.Now().Unix()
	var models []gin.H
	for _, p := range cfg.PublicProviders {
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			// If this model is access-restricted, only include it for allowed itcodes.
			if cfg.IsModelRestrictedForItcode(p.Name, m, itcode) {
				continue
			}
			models = append(models, gin.H{
				"id":       m,
				"object":   "model",
				"created":  now,
				"owned_by": p.Name,
			})
		}
	}
	return models
}

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, provider *config.PublicProvider,
	model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, reqBody []byte) {

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()

	// acc merges usage across SSE events field-by-field (see internal/usage).
	var acc usage.Accumulator
	var outCounts tokenest.Counts
	// linesBuf accumulates bytes until a complete SSE line is formed
	var linesBuf []byte
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
			linesBuf = append(linesBuf, buf[:n]...)
			// parse complete lines from linesBuf, keep remainder
			linesBuf = consumeSSELines(linesBuf, &acc, &outCounts)
		}
		if err != nil {
			break
		}
	}
	// parse any remaining bytes
	consumeSSELines(linesBuf, &acc, &outCounts)

	// Estimate only when the stream carried no usage data at all.
	if !acc.SawUsage {
		acc.Input = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
		acc.Output = outCounts.Estimate(tokenest.Default)
	}

	h.emitUsage(keyInfo, keyStr, provider, model, statusCode, acc.Input, acc.Output, acc.CacheRead, acc.CacheWrite, time.Since(start), c.ClientIP())
}

// consumeSSELines parses complete newline-terminated SSE lines from data,
// merging usage into acc on every data: event that reports it.
// Returns the unconsumed remainder (partial last line).
func consumeSSELines(data []byte, acc *usage.Accumulator, outCounts *tokenest.Counts) []byte {
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := bytes.TrimSpace(data[:idx])
		data = data[idx+1:]

		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		acc.Add(usage.ParseTokens(payload))
		if t := tokenest.ExtractDeltaText(payload); t != "" {
			outCounts.Add(t)
		}
	}
	return data
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, provider *config.PublicProvider,
	model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, reqBody []byte) {

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read public provider response: %v", err)
		return
	}
	c.Writer.Write(respBody)

	in, out, cacheRead, cacheWrite := usage.ParseTokens(respBody)
	if in == 0 && out == 0 && cacheRead == 0 && cacheWrite == 0 {
		in = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
		out = tokenest.EstimateString(tokenest.ExtractResponseText(respBody), tokenest.Default)
	}
	h.emitUsage(keyInfo, keyStr, provider, model, statusCode, in, out, cacheRead, cacheWrite, time.Since(start), c.ClientIP())
}

// costUSD estimates cost based on the provider's model_pricing config,
// including prompt-cache tokens when the provider reports them.
func costUSD(provider *config.PublicProvider, model string, inputTokens, outputTokens, cacheRead, cacheWrite int) float64 {
	if provider == nil || provider.ModelPricing == nil {
		return 0
	}
	pricing, ok := provider.ModelPricing[model]
	if !ok {
		return 0
	}
	// Entries without explicit cache prices derive them from the standard
	// ratios (read = 10% of input, write = 125%).
	if pricing.CacheRead == 0 && pricing.CacheWrite == 0 {
		pricing.CacheRead = pricing.Input * 0.1
		pricing.CacheWrite = pricing.Input * 1.25
	}
	return (float64(inputTokens)*pricing.Input + float64(outputTokens)*pricing.Output +
		float64(cacheRead)*pricing.CacheRead + float64(cacheWrite)*pricing.CacheWrite) / 1_000_000
}

func (h *Handler) emitUsage(keyInfo interface{}, keyStr string, provider *config.PublicProvider,
	model string, statusCode, inputTokens, outputTokens, cacheRead, cacheWrite int, latency time.Duration, clientIP string) {

	if h.collector == nil || keyInfo == nil {
		return
	}
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok {
		return
	}

	total := inputTokens + outputTokens
	cost := costUSD(provider, model, inputTokens, outputTokens, cacheRead, cacheWrite)

	var city string
	var isHQ bool
	if h.ipGeo != nil {
		city, isHQ = h.ipGeo.Observe(clientIP)
	}

	h.collector.Emit(stats.Record{
		UserID:           info.UserID,
		GroupID:          info.GroupID,
		APIKeyID:         info.KeyID,
		KeyStr:           keyStr,
		Model:            model,
		Backend:          "public:" + provider.Name,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		TotalTokens:      total,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostUSD:          cost,
		StatusCode:       statusCode,
		Latency:          latency,
		IP:               clientIP,
		City:             city,
		IsHQ:             isHQ,
	})

	// Accumulate daily cost for per-user quota tracking
	if cost > 0 && statusCode < 400 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
}

// CheckDailyLimit enforces the shared per-user daily spending quota on the
// public channel. Public-provider spend accumulates into the same daily
// bucket as the backend channel, so exceeding the limit rejects public
// requests too — with the same 429 body the backend channel returns.
// Returns true when the request was rejected (caller must stop processing).
func (h *Handler) CheckDailyLimit(c *gin.Context, keyInfo interface{}) bool {
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok || info == nil {
		return false
	}
	cfg := h.getCfg()
	globalMax := 0.0
	if cfg != nil {
		globalMax = cfg.BackendDailyMax
	}
	effectiveLimit := quota.ResolveBackendDaily(h.keyStore, info.UserID, globalMax)
	if effectiveLimit <= 0 {
		return false
	}
	todayCost := h.keyStore.GetDailyCost(info.UserID)
	if todayCost < effectiveLimit {
		return false
	}
	logger.Warnf("daily backend limit exceeded (public channel): user_id=%d today=%.4f limit=%.4f",
		info.UserID, todayCost, effectiveLimit)
	c.JSON(http.StatusTooManyRequests, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "rate_limit_error",
			"message": "Daily backend spending limit exceeded. Please try again tomorrow.",
		},
	})
	return true
}

// buildTargetURL constructs the full upstream URL.
// baseURL might be "https://api.moonshot.cn/v1", path might be "/v1/chat/completions"
// We need to avoid double /v1, so we strip /v1 prefix from path if baseURL already ends with /v1.
func buildTargetURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")

	// If base already ends with /v1 and path starts with /v1, strip from path
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}

	return base + path
}
