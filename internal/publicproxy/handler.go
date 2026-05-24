package publicproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
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

// Handler transparently forwards requests to public third-party providers
// (e.g. Kimi, MiniMax) based on the requested model name.
type Handler struct {
	collector *stats.Collector
	keyStore  *auth.KeyStore
	mu        sync.RWMutex
	cfg       *config.Config
	client    *http.Client
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

// MatchModel checks if the model is served by a public provider.
// Returns the provider if found, nil otherwise.
func (h *Handler) MatchModel(model string) *config.PublicProvider {
	cfg := h.getCfg()
	if cfg == nil {
		return nil
	}
	return cfg.LookupPublicProvider(model)
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
		h.streamResponse(c, resp, provider, model, keyInfo, keyStr, resp.StatusCode, start)
	} else {
		h.bufferResponse(c, resp, provider, model, keyInfo, keyStr, resp.StatusCode, start)
	}
}

// Models returns the list of public models as OpenAI-compatible model objects.
func (h *Handler) Models() []gin.H {
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
	model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time) {

	// Set log context for the request logger middleware
	c.Set("log_provider", provider.Name)
	c.Set("log_model", model)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()

	var totalIn, totalOut int
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
			linesBuf = consumeSSELines(linesBuf, &totalIn, &totalOut)
		}
		if err != nil {
			break
		}
	}
	// parse any remaining bytes
	consumeSSELines(linesBuf, &totalIn, &totalOut)

	c.Set("log_input_tokens", totalIn)
	c.Set("log_output_tokens", totalOut)
	h.emitUsage(keyInfo, keyStr, provider, model, statusCode, totalIn, totalOut, time.Since(start))
}

// consumeSSELines parses complete newline-terminated SSE lines from data,
// updating in/out token counters on every data: event that contains usage.
// Returns the unconsumed remainder (partial last line).
func consumeSSELines(data []byte, in, out *int) []byte {
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
		i, o := parseBodyTokens(payload)
		if i > 0 {
			*in = i
		}
		if o > 0 {
			*out = o
		}
	}
	return data
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, provider *config.PublicProvider,
	model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time) {

	// Set log context for the request logger middleware
	c.Set("log_provider", provider.Name)
	c.Set("log_model", model)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read public provider response: %v", err)
		return
	}
	c.Writer.Write(respBody)

	in, out := parseBodyTokens(respBody)
	c.Set("log_input_tokens", in)
	c.Set("log_output_tokens", out)
	h.emitUsage(keyInfo, keyStr, provider, model, statusCode, in, out, time.Since(start))
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

// costUSD estimates cost based on the provider's model_pricing config.
func costUSD(provider *config.PublicProvider, model string, inputTokens, outputTokens int) float64 {
	if provider == nil || provider.ModelPricing == nil {
		return 0
	}
	pricing, ok := provider.ModelPricing[model]
	if !ok {
		return 0
	}
	return (float64(inputTokens)*pricing.Input + float64(outputTokens)*pricing.Output) / 1_000_000
}

func (h *Handler) emitUsage(keyInfo interface{}, keyStr string, provider *config.PublicProvider,
	model string, statusCode, inputTokens, outputTokens int, latency time.Duration) {

	if h.collector == nil || keyInfo == nil {
		return
	}
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok {
		return
	}

	total := inputTokens + outputTokens
	cost := costUSD(provider, model, inputTokens, outputTokens)

	h.collector.Emit(stats.Record{
		UserID:       info.UserID,
		GroupID:      info.GroupID,
		APIKeyID:     info.KeyID,
		KeyStr:       keyStr,
		Provider:     provider.Name,
		Model:        model,
		BackendName:  "",
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  total,
		CostUSD:      cost,
		StatusCode:   statusCode,
		Latency:      latency,
	})

	// Accumulate daily cost for per-user quota tracking
	if cost > 0 && statusCode < 400 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
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
