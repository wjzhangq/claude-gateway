package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/classify"
	"github.com/wjzhangq/claude-gateway/internal/ipgeo"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/quota"
	"github.com/wjzhangq/claude-gateway/internal/sanitize"
	"github.com/wjzhangq/claude-gateway/internal/stats"
	"github.com/wjzhangq/claude-gateway/internal/tokenest"
	"github.com/wjzhangq/claude-gateway/internal/usage"
	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// Handler forwards requests to upstream Claude backends.
type Handler struct {
	lb                *LoadBalancer
	collector         *stats.Collector
	keyStore          *auth.KeyStore
	config            *config.Config
	mu                sync.RWMutex
	modelReplacements map[string]string
	fallbackClient    *http.Client
	ipGeo             *ipgeo.Store

	// Web-search simulation (backend channel). webSearchCfg is the current
	// config; searxng is a lazily-built client rebuilt when the config changes.
	// Both are guarded by mu.
	webSearchCfg config.WebSearchConfig
	searxng      *websearch.Client
}

func NewHandler(lb *LoadBalancer, collector *stats.Collector, keyStore *auth.KeyStore, cfg *config.Config, modelReplacements map[string]string) *Handler {
	h := &Handler{
		lb:                lb,
		collector:         collector,
		keyStore:          keyStore,
		config:            cfg,
		modelReplacements: modelReplacements,
		fallbackClient:    &http.Client{Timeout: 5 * time.Minute},
	}
	if cfg != nil {
		h.webSearchCfg = cfg.WebSearch
		if cfg.WebSearch.Enabled && cfg.WebSearch.SearchURL != "" {
			h.searxng = websearch.NewClient(cfg.WebSearch)
		}
	}
	return h
}

// SetIPGeo attaches an IP → city/HQ store used to tag usage records. Nil-safe:
// when unset, usage records carry empty IP/city and is_hq=false.
func (h *Handler) SetIPGeo(s *ipgeo.Store) { h.ipGeo = s }

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

// isValidHealthModel reports whether an error on a request for this model should
// count against a Claude backend's health (US6, FR-015..FR-017). Only the
// sonnet/haiku/opus families are real signal for a Claude backend; errors on
// other/unknown models say nothing about the backend's ability to serve the
// traffic we actually route, so they must not eject or degrade it.
//
// An empty/undeterminable model returns true (count it): treating unknown as
// "ignore" risks keeping a genuinely broken backend routable, which is the
// worse failure (FR-017 documented default).
func isValidHealthModel(model string) bool {
	if strings.TrimSpace(model) == "" {
		return true
	}
	m := strings.ToLower(model)
	return strings.Contains(m, "sonnet") ||
		strings.Contains(m, "opus") ||
		strings.Contains(m, "haiku")
}

// stripThinkingSuffix maps a "-thinking" model pseudo-variant to its base model
// name. claude-cli appends "-thinking" (e.g. claude-sonnet-4-5-20250929-thinking)
// to signal extended thinking, but upstream distributors only register the base
// model, so the variant resolves to "No available channel" (503). Extended
// thinking is driven by the request's "thinking" body field, not the model name,
// so dropping the suffix preserves behaviour while routing to a real channel.
// Returns (base, true) when the suffix was present, else ("", false).
func stripThinkingSuffix(model string) (string, bool) {
	const suffix = "-thinking"
	if strings.HasSuffix(strings.ToLower(model), suffix) {
		return model[:len(model)-len(suffix)], true
	}
	return "", false
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
	// Fast-path: /v1/messages/count_tokens is answered locally with a token
	// estimate instead of being forwarded. Most upstream distributors don't
	// implement this endpoint and reply "Invalid URL (POST
	// /v1/messages/count_tokens)" (a 404), which claude-cli calls before every
	// message — the single biggest source of avoidable backend errors. Serving
	// it here removes that class entirely.
	if strings.HasSuffix(upstreamPath, "/count_tokens") {
		h.handleCountTokens(c)
		return
	}

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

	// Strip per-session steganographic date markers before forwarding.
	// Only applied to Claude models; the sanitizer's fast path makes this a
	// cheap no-op for requests that don't carry the marker.
	if isClaudeModel(reqModel) {
		if cleaned, ok := sanitize.Body(body); ok {
			body = cleaned
			logger.Infof("stegano marker cleaned on backend channel: model=%s ua=%s", reqModel, c.GetHeader("User-Agent"))
		}
	}

	// Detect OpenClaw request
	isOpenClaw := detectOpenClaw(c.GetHeader("User-Agent"), body)
	if isOpenClaw {
		c.Set("is_openclaw", true)
	}

	// Detect Hermes client (counts as lobster traffic)
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

	// Lobster (openclaw/hermes) handling for Claude models
	if (isOpenClaw || isHermes) && isClaudeModel(reqModel) {
		h.mu.RLock()
		cfg := h.config
		h.mu.RUnlock()

		whitelisted := false
		if cfg != nil {
			if info, ok := keyInfo.(*auth.KeyInfo); ok {
				whitelisted = cfg.IsLobsterWhitelisted(info.Itcode)
			}
		}

		if !whitelisted {
			if cfg != nil && cfg.LobsterAutoForward {
				// Auto-forward to fallback model
				logger.Infof("lobster auto-forward: user=%v model=%s -> fallback", func() interface{} {
					if info, ok := keyInfo.(*auth.KeyInfo); ok {
						return info.Itcode
					}
					return "?"
				}(), reqModel)

				start := time.Now()
				resp, fbModel, fwdErr := h.doRequestWithDowngrade(c, backend, upstreamPath, body, reqModel, isOpenClaw, isHermes)
				if fwdErr != nil || resp == nil {
					c.JSON(http.StatusBadGateway, gin.H{"error": "fallback request failed"})
					return
				}
				defer resp.Body.Close()

				for k, vv := range resp.Header {
					for _, v := range vv {
						c.Header(k, v)
					}
				}
				c.Header("X-Gateway-Model", fbModel)
				c.Status(resp.StatusCode)

				isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
				if isStream {
					h.streamResponse(c, resp, backend.Name, fbModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, true, body)
				} else {
					h.bufferResponse(c, resp, backend.Name, fbModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, true, body)
				}
				return
			}
			// lobster_auto_forward is off — block as before
			logger.Warnf("OpenClaw/Hermes client blocked on backend channel (claude model): user_id=%v model=%s ua=%s",
				func() interface{} {
					if ki, ok := keyInfo.(*auth.KeyInfo); ok {
						return ki.UserID
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
		// whitelisted — fall through to normal routing
		logger.Infof("lobster whitelisted user on backend channel: model=%s", reqModel)
	} else if isOpenClaw {
		logger.Infof("OpenClaw client allowed on backend channel (non-claude model): model=%s", reqModel)
	}

	// Check if we're in the downgraded period (skip original model and go directly to GPT)
	inDowngradedPeriod := autoDowngrade && keyStr != "" && h.keyStore.IsDowngraded(keyStr)

	// Check per-user daily backend spending limit.
	// Resolution is centralized in internal/quota so the enforced limit always
	// equals the limit shown on the dashboard / /api/quota / response headers.
	if info, ok := keyInfo.(*auth.KeyInfo); ok {
		h.mu.RLock()
		globalMax := 0.0
		if h.config != nil {
			globalMax = h.config.BackendDailyMax
		}
		h.mu.RUnlock()

		effectiveLimit := quota.ResolveBackendDaily(h.keyStore, info.UserID, globalMax)
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

	// Apply locked model override: if the key has a locked model set, force it
	if info, ok := keyInfo.(*auth.KeyInfo); ok && info.LockedModel != "" {
		body = ReplaceModelInBody(body, reqModel, info.LockedModel)
		reqModel = info.LockedModel
	}

	// Normalize "-thinking" model variants to their base model. Upstream
	// distributors register the base model (e.g. claude-sonnet-4-5-20250929) but
	// not the "-thinking" pseudo-variant claude-cli sends, so the variant returns
	// "No available channel for model ...-thinking" (503). Stripping the suffix
	// here routes to the real channel; the request's own "thinking" body field is
	// left untouched, so extended thinking still works when the model supports it.
	if base, ok := stripThinkingSuffix(reqModel); ok {
		body = ReplaceModelInBody(body, reqModel, base)
		logger.Infof("thinking variant mapped to base model: %s -> %s", reqModel, base)
		reqModel = base
	}

	// Apply model replacements: if request model contains a configured pattern, replace it
	h.mu.RLock()
	replacements := h.modelReplacements
	h.mu.RUnlock()
	for pattern, replacement := range replacements {
		if strings.Contains(reqModel, pattern) {
			body = ReplaceModelInBody(body, reqModel, replacement)
			reqModel = replacement
			break
		}
	}

	// Web-search simulation (backend channel only): if this is a /v1/messages
	// request that declares Anthropic's web_search server tool and the feature
	// is enabled, run the gateway agent loop instead of a plain passthrough.
	// The loop rewrites the server tool to a client tool, resolves the model's
	// searches against SearXNG, and emits native server_tool_use +
	// web_search_tool_result blocks. Any other request falls through untouched.
	h.mu.RLock()
	wsEnabled := h.webSearchCfg.Enabled && h.searxng != nil
	h.mu.RUnlock()
	if wsEnabled && strings.HasSuffix(upstreamPath, "/messages") && websearch.DetectInBody(body) {
		h.handleWithWebSearch(c, backend, upstreamPath, body, reqModel, keyInfo, keyStr, time.Now(), isOpenClaw, isHermes)
		return
	}

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
		// Normal path with failover. Any upstream outcome the caller cannot fix —
		// a transport failure, 5xx, 429, or the auth/quota rejections that Claude
		// Code renders as "Please run /login" — is retried on the next backend
		// instead of being handed back as the client's error. Only a genuine
		// request-level 4xx (400/404/422/…) is returned as-is, since every node
		// would reject it identically.
		fo := newFailoverState(backend, upstreamPath, start)
		for {
			attemptStart := time.Now()
			resp, err = h.doRequest(c, fo.backend, upstreamPath, fo.targetURL, body)

			// Client aborted the request (context canceled/deadline): this is not
			// a backend fault. Don't count it against backend health and don't log
			// it as an error — there is no client left to serve.
			if err != nil && IsClientCanceled(err) {
				fo.backend.RecordResult(ErrCanceled)
				logger.Infof("client canceled request: backend=%s model=%s", fo.backend.Name, reqModel)
				return
			}

			var peek []byte
			verdict := transportVerdict
			if err == nil {
				peek, verdict = classifyUpstreamResponse(resp)
			}
			if !verdict.Retry {
				restoreBody(resp, peek)
				break
			}

			next := fo.next(h.lb, verdict, time.Now())
			if next == nil {
				if err == nil {
					restoreBody(resp, peek)
					logger.Warnf("failover exhausted (backend=%s reason=%s attempts=%d): returning %d to client",
						fo.backend.Name, verdict.Reason, fo.failovers+1, resp.StatusCode)
				}
				break // keep this outcome; the final attempt is recorded below
			}

			// Abandon this backend: release its connection, record the failure
			// against its health, then retry the same body on the next node.
			status := 0
			if resp != nil {
				status = resp.StatusCode
				resp.Body.Close()
			}
			h.recordFailedAttempt(fo.backend, verdict, status, time.Since(attemptStart).Milliseconds(), reqModel)
			logger.Warnf("backend %s failed (reason=%s status=%d): failing over to %s",
				fo.backend.Name, verdict.Reason, status, next.Name)
			fo.advance(next, upstreamPath)
		}
		backend = fo.backend

		if err != nil {
			// Transport failure: count it against health only for a valid-health
			// model (US6, FR-016); client-canceled was already handled above.
			if isValidHealthModel(reqModel) {
				backend.RecordResult(ClassifyError(0, err))
			}
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
				logBackendAnomaly("backend request failed", keyInfo, backend.Name, reqModel, 0, body, nil, time.Since(start), err)
				c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
				return
			}
		}
	}

	defer resp.Body.Close()
	// Record the outcome. RecordRequest first so lastHTTPCode is fresh before the
	// health accounting in RecordResultDetailed reads it. On a 429, parse the
	// Retry-After response header into an absolute expiry so the TTL policy can
	// honor it instead of the 30s base.
	backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds())
	var retryAfterUnix int64
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfterUnix = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	// Model-scoped health (US6): only sonnet/haiku/opus errors count against a
	// Claude backend. For other/unknown models, record the outcome as a no-op
	// class so state/consecErr/TTL are untouched.
	if isValidHealthModel(reqModel) {
		backend.RecordResultDetailed(ClassifyError(resp.StatusCode, nil), retryAfterUnix, false)
	} else {
		backend.RecordResultDetailed(ErrClient, 0, false)
	}

	// Check if we need to retry with downgrade (response indicates failure)
	if autoDowngrade && !isDowngraded && resp.StatusCode >= 400 {
		resp.Body.Close()
		backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds()) // record the failure first

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

	// Claude Code renders a pre-charge 403 as "Please run /login" and does not
	// retry it. Rewrite only the client-facing response after failover and
	// downgrade. Health (recorded above) and usage stay on the original status.
	loggedStatus := resp.StatusCode
	if maybeRewriteClaudeLoginQuota(c.GetHeader("User-Agent"), resp) {
		logger.Warnf("rewrote login-quota 403 as 529 for claude client: backend=%s model=%s", backend.Name, reqModel)
	}

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
		var quotaUSD float64
		if info.Channel == "aws" {
			quotaUSD = info.AWSDailyQuotaUSD
		} else {
			// Same resolution as enforcement and dashboard — DB override first,
			// then global backend_daily_max (0 = unlimited).
			h.mu.RLock()
			globalMax := 0.0
			if h.config != nil {
				globalMax = h.config.BackendDailyMax
			}
			h.mu.RUnlock()
			quotaUSD = quota.ResolveBackendDaily(h.keyStore, info.UserID, globalMax)
		}
		c.Header("X-Gateway-Daily-Quota", strconv.FormatFloat(quotaUSD, 'f', 4, 64))
	}

	c.Status(resp.StatusCode)

	// Stream or buffer. loggedStatus is the upstream code (403 for a rewritten
	// login-quota error) so usage and anomaly logs do not record the 529.
	isStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStream {
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, loggedStatus, start, isOpenClaw, isHermes, isDowngraded, body)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, loggedStatus, start, isOpenClaw, isHermes, isDowngraded, body)
	}
}

// handleCountTokens answers POST /v1/messages/count_tokens locally with an
// estimated input token count, matching Anthropic's response shape
// ({"input_tokens": N}). It never touches an upstream backend. The estimate
// reuses the same tokenest heuristic used as the usage fallback elsewhere, so
// counts are consistent with what we bill.
func (h *Handler) handleCountTokens(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"type":  "error",
			"error": gin.H{"type": "invalid_request_error", "message": "read request body failed"},
		})
		return
	}
	n := tokenest.EstimateString(tokenest.ExtractRequestText(body), tokenest.Default)
	if n < 1 {
		n = 1
	}
	c.Header("X-Gateway-Count-Tokens", "estimated")
	c.JSON(http.StatusOK, gin.H{"input_tokens": n})
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
// the request is forwarded to that provider. Otherwise returns an error.
// Returns the response, the actual fallback model name used, and any error.
func (h *Handler) doRequestWithDowngrade(c *gin.Context, backend *Backend, upstreamPath string, body []byte, originalModel string, isOpenClaw, isHermes bool) (*http.Response, string, error) {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()

	// Try config-based fallback to public provider
	if cfg != nil && cfg.Fallback != "" {
		fallbackModel := cfg.Fallback
		provider := cfg.LookupPublicProvider(fallbackModel, "")
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

	return nil, "", fmt.Errorf("no fallback configured")
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

// maxFailovers caps how many alternate backends a single request will try after
// a retryable upstream failure. Together with failoverBudgetSecs it bounds
// worst-case latency when several nodes are failing at once.
const maxFailovers = 3

// transportVerdict is the outcome for an attempt that never produced a response.
var transportVerdict = upstreamVerdict{Retry: true, Reason: "transport", Class: ErrTransport}

// failoverState tracks one request's walk across backends so every upstream
// path applies the same stop conditions.
type failoverState struct {
	backend   *Backend
	targetURL string
	tried     map[string]bool
	failovers int
	deadline  time.Time
}

func newFailoverState(b *Backend, upstreamPath string, start time.Time) *failoverState {
	return &failoverState{
		backend:   b,
		targetURL: strings.TrimRight(b.URL, "/") + upstreamPath,
		tried:     map[string]bool{b.Name: true},
		deadline:  start.Add(failoverBudgetSecs * time.Second),
	}
}

// next returns the backend to retry on, or nil when the chain must stop: no
// untried alternate, the per-request cap is hit, the wall-clock budget elapsed
// (FR-022), or a quota cascade is in progress, where walking the remaining nodes
// only adds upstream load at the worst moment (FR-023).
func (s *failoverState) next(lb *LoadBalancer, v upstreamVerdict, now time.Time) *Backend {
	if s.failovers >= maxFailovers || now.After(s.deadline) {
		return nil
	}
	if v.Quota && lb.QuotaCascadeActive() {
		return nil
	}
	return lb.PickExcluding(s.tried)
}

// advance moves the chain onto b.
func (s *failoverState) advance(b *Backend, upstreamPath string) {
	s.failovers++
	s.backend = b
	s.tried[b.Name] = true
	s.targetURL = strings.TrimRight(b.URL, "/") + upstreamPath
}

// parseRetryAfter parses an HTTP Retry-After header into an absolute unix-second
// expiry, supporting both the delta-seconds form ("Retry-After: 120") and the
// HTTP-date form ("Retry-After: Fri, 31 Dec 2027 23:59:59 GMT"). Returns 0 when
// the header is absent or unparseable, or resolves to a time in the past.
func ParseRetryAfter(header string, now time.Time) int64 {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	// Delta-seconds form.
	if secs, err := strconv.ParseInt(header, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return now.Unix() + secs
	}
	// HTTP-date form.
	if t, err := http.ParseTime(header); err == nil {
		if t.After(now) {
			return t.Unix()
		}
	}
	return 0
}

// upstreamBlock categorizes an upstream rejection body that carries no useful
// signal in its status code alone.
type upstreamBlock int

const (
	blockNone           upstreamBlock = iota
	blockSoftLogin                    // pre-charge / re-login prompt; usually transient
	blockQuotaExhausted               // the node's upstream account is out of balance or suspended
)

// quotaExhaustedMarkers identify a node whose upstream account has actually run
// dry (Anthropic/new-api "exceeded_current_quota_error" and friends). These earn
// the long quota isolation, because retrying the node soon is pointless.
var quotaExhaustedMarkers = []string{
	"exceeded_current_quota_error",
	"insufficient balance",
	"suspended due to",
}

// softLoginMarkers identify a pre-charge / session rejection that new-api style
// distributors return as a 403, and that Claude Code renders as
// "Please run /login". It usually clears on its own, so it earns the ordinary
// short 403 cooldown rather than the multi-hour quota isolation.
var softLoginMarkers = []string{
	"预扣费",
	"用户剩余额度",
	"额度不足",
	"余额不足",
	"请先登录",
	"/login",
	"api error: 403",
	"token has expired",
}

// classifyBlockBody inspects an upstream 401/403/429 body for the phrasing that
// distinguishes an exhausted account from a transient login/pre-charge
// rejection. Matching is case-insensitive and gzip-aware, because clients such
// as claude-cli send Accept-Encoding: gzip and the upstream error body comes
// back compressed.
func classifyBlockBody(header http.Header, body []byte) upstreamBlock {
	s := strings.ToLower(string(decodeMaybeGzip(header, body)))
	for _, m := range quotaExhaustedMarkers {
		if strings.Contains(s, m) {
			return blockQuotaExhausted
		}
	}
	for _, m := range softLoginMarkers {
		if strings.Contains(s, m) {
			return blockSoftLogin
		}
	}
	return blockNone
}

// decodeMaybeGzip returns the decompressed body when the headers declare gzip,
// and the original bytes otherwise (including when decompression fails).
func decodeMaybeGzip(header http.Header, body []byte) []byte {
	if header == nil || !strings.Contains(strings.ToLower(header.Get("Content-Encoding")), "gzip") {
		return body
	}
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return body
	}
	defer r.Close()
	decoded, err := io.ReadAll(io.LimitReader(r, maxPeekBytes))
	if err != nil || len(decoded) == 0 {
		return body
	}
	return decoded
}

// maxPeekBytes bounds how much of an upstream error body is read for
// classification. Error payloads are small; anything larger is streamed through
// untouched by restoreBody.
const maxPeekBytes = 8192

// peekBlockBody reads the head of an upstream error body and classifies it. The
// consumed bytes are returned so restoreBody can put them back in front of the
// unread remainder.
func peekBlockBody(resp *http.Response) ([]byte, upstreamBlock) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPeekBytes))
	if err != nil {
		return body, blockNone
	}
	return body, classifyBlockBody(resp.Header, body)
}

// restoreBody puts peeked bytes back in front of the unread remainder so the
// full response can still be forwarded to the client. Replacing the body with
// only the peek would silently truncate any payload longer than maxPeekBytes.
func restoreBody(resp *http.Response, peek []byte) {
	if resp == nil || peek == nil {
		return
	}
	rest := resp.Body
	resp.Body = &rewoundBody{r: io.MultiReader(bytes.NewReader(peek), rest), c: rest}
}

type rewoundBody struct {
	r io.Reader
	c io.Closer
}

func (b *rewoundBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *rewoundBody) Close() error               { return b.c.Close() }

// upstreamVerdict is the gateway's decision about one upstream attempt.
type upstreamVerdict struct {
	Retry  bool       // try the next backend instead of returning this to the client
	Reason string     // compact label for logs
	Class  ErrorClass // how the attempt counts against the node's health
	Quota  bool       // the node reported an exhausted upstream account
}

// classifyUpstreamResponse decides whether an upstream response should be failed
// over to the next node, and how it counts against the current node's health.
// It returns any bytes consumed while inspecting the body so the caller can
// restore them via restoreBody.
//
// Everything the caller cannot act on is retryable: a 401 or 403 is the node's
// own upstream credential or balance failing, which a different node does not
// share, and 408/429/5xx are transient upstream conditions. Only a genuine
// request-level 4xx is returned as-is, because every node would reject it the
// same way.
func classifyUpstreamResponse(resp *http.Response) ([]byte, upstreamVerdict) {
	if resp == nil {
		return nil, upstreamVerdict{}
	}
	switch code := resp.StatusCode; {
	case code < 400:
		return nil, upstreamVerdict{}

	case code == http.StatusUnauthorized:
		// The gateway's own key check already passed upstream of here, so a 401
		// is always the node's upstream credential. Claude Code surfaces it as
		// "Please run /login" and gives up, so never hand it to the client while
		// another node is available.
		return nil, upstreamVerdict{Retry: true, Reason: "auth_401", Class: ErrAuth}

	case code == http.StatusForbidden:
		peek, block := peekBlockBody(resp)
		switch block {
		case blockQuotaExhausted:
			return peek, upstreamVerdict{Retry: true, Reason: "quota_403", Class: ErrForbidden, Quota: true}
		case blockSoftLogin:
			return peek, upstreamVerdict{Retry: true, Reason: "login_403", Class: ErrForbidden}
		}
		return peek, upstreamVerdict{Retry: true, Reason: "forbidden_403", Class: ErrForbidden}

	case code == http.StatusRequestTimeout:
		return nil, upstreamVerdict{Retry: true, Reason: "timeout_408", Class: ErrServer}

	case code == http.StatusTooManyRequests:
		peek, block := peekBlockBody(resp)
		if block == blockQuotaExhausted {
			return peek, upstreamVerdict{Retry: true, Reason: "quota_429", Class: ErrRateLimit, Quota: true}
		}
		return peek, upstreamVerdict{Retry: true, Reason: "rate_limit_429", Class: ErrRateLimit}

	case code >= 500:
		return nil, upstreamVerdict{Retry: true, Reason: "server_5xx", Class: ErrServer}
	}
	return nil, upstreamVerdict{}
}

// recordFailedAttempt books an abandoned attempt against the node's health
// before the request moves on. A node reporting an exhausted account is
// circuit-broken for the long quota cooldown; everything else takes the ordinary
// cooldown for its class, so a transient login rejection costs minutes rather
// than hours.
func (h *Handler) recordFailedAttempt(backend *Backend, verdict upstreamVerdict, status int, latencyMs int64, reqModel string) {
	if status > 0 {
		backend.RecordRequest(status, latencyMs)
	}
	if verdict.Quota {
		h.lb.MarkQuotaExhausted(backend.Name)
	}
	if !isValidHealthModel(reqModel) {
		backend.RecordResultDetailed(ErrClient, 0, false)
		return
	}
	backend.RecordResultDetailed(verdict.Class, 0, false)
}

// isLoginQuotaBody reports whether an upstream 401/403 body is a pre-charge or
// balance failure rather than a generic rejection.
func isLoginQuotaBody(body []byte) bool {
	return classifyBlockBody(nil, body) != blockNone
}

// statusOverloaded is Anthropic's overloaded status. Claude Code retries it;
// it does not retry 403.
const statusOverloaded = 529

// overloadedErrorBody is the Anthropic overloaded error Claude Code retries.
var overloadedErrorBody = []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)

// isClaudeCodeUA reports whether the client is Claude Code (User-Agent contains
// "claude", e.g. "claude-cli/2.1.0 (external, cli)").
func isClaudeCodeUA(userAgent string) bool {
	return strings.Contains(strings.ToLower(userAgent), "claude")
}

// maybeRewriteClaudeLoginQuota replaces an upstream credential or pre-charge
// rejection with 529 overloaded when the client is Claude Code. It runs only
// after failover has exhausted every node, so the client is being told the whole
// pool is unavailable, not that its own key is wrong — 529 says that and gets
// retried, where 401/403 makes Claude Code print "Please run /login" and stop.
// Health and usage must already have been recorded from the original status. A
// non-matching response is left intact, including its body. Returns true when
// the response was rewritten.
func maybeRewriteClaudeLoginQuota(ua string, resp *http.Response) bool {
	if resp == nil || !isClaudeCodeUA(ua) {
		return false
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	// Leave the original body for the caller to Close (both call sites defer it).
	// The bytes are copied onto a new reader so the client write sees either the
	// original payload or the overloaded replacement.
	orig := resp.Body
	if orig == nil {
		return false
	}
	body, err := io.ReadAll(orig)
	// A 401 is always the node's upstream credential, whatever the body says; a
	// 403 only qualifies when the body names a login/balance failure, so a
	// genuine permission error still reaches the client unchanged.
	rewrite := resp.StatusCode == http.StatusUnauthorized ||
		classifyBlockBody(resp.Header, body) != blockNone
	if err != nil || !rewrite {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		return false
	}
	resp.StatusCode = statusOverloaded
	resp.Status = fmt.Sprintf("%d Overloaded", statusOverloaded)
	resp.Body = io.NopCloser(bytes.NewReader(overloadedErrorBody))
	resp.ContentLength = int64(len(overloadedErrorBody))
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Del("Content-Encoding")
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(overloadedErrorBody)))
	return true
}

// rewriteClaudeLoginQuotaBody is the already-buffered form of
// maybeRewriteClaudeLoginQuota, used when the upstream error body was read
// before the client response is written.
func rewriteClaudeLoginQuotaBody(ua string, status int, body []byte) (int, []byte, bool) {
	if !isClaudeCodeUA(ua) {
		return status, body, false
	}
	switch {
	case status == http.StatusUnauthorized:
	case status == http.StatusForbidden && isLoginQuotaBody(body):
	default:
		return status, body, false
	}
	return statusOverloaded, overloadedErrorBody, true
}

const streamReadBufSize = 4096

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, isOpenClaw, isHermes, isDowngraded bool, reqBody []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()
	// acc merges usage across SSE events field-by-field. Anthropic reports
	// input_tokens (+ cache tokens) once in message_start, then only
	// output_tokens in message_delta — a wholesale overwrite would lose input.
	var acc usage.Accumulator
	var outCounts tokenest.Counts
	buf := make([]byte, streamReadBufSize)
	// partial holds bytes of an incomplete SSE line across reads
	var partial []byte
	var respBuf []byte
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// Sanitize upstream domains from error responses (status >= 400)
			if statusCode >= 400 {
				chunk = sanitize.RemoveUpstreamDomains(chunk)
			}
			c.Writer.Write(chunk)
			if canFlush {
				flusher.Flush()
			}
			respBuf = append(respBuf, chunk...)
			// Parse SSE lines incrementally for token counting
			partial = append(partial, chunk...)
			for {
				idx := bytes.IndexByte(partial, '\n')
				if idx < 0 {
					break
				}
				line := bytes.TrimSpace(partial[:idx])
				partial = partial[idx+1:]
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
					continue
				}
				if i, o, cr, cw := usage.ParseTokens(payload); i > 0 || o > 0 || cr > 0 || cw > 0 {
					acc.Add(i, o, cr, cw)
				}
				if t := tokenest.ExtractDeltaText(payload); t != "" {
					outCounts.Add(t)
				}
			}
		}
		if err != nil {
			break
		}
	}

	// Estimate only when the stream carried NO usage data at all. A legitimate
	// cache-hit response can report input_tokens=0 with large cache_read — that
	// must not trigger estimation (which would bill the full prompt text).
	if !acc.SawUsage {
		acc.Input = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
		acc.Output = outCounts.Estimate(tokenest.Default)
		logger.Warnf("stream carried no usage, estimated tokens: backend=%s model=%s in=%d out=%d",
			backendName, model, acc.Input, acc.Output)
	}
	// rawZero after fallback: a genuinely empty stream (no request text, no
	// streamed delta content). This avoids flagging truncated-but-successful
	// streams — where usage tokens never arrived but real deltas did — as
	// "backend zero tokens".
	rawZero := acc.Input == 0 && acc.Output == 0

	latency := time.Since(start)
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, acc.Input, acc.Output, acc.CacheRead, acc.CacheWrite, latency, isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"), c.ClientIP(), reqBody)

	if statusCode >= 400 || rawZero {
		msg := "backend zero tokens"
		if statusCode >= 400 {
			msg = "backend error response"
		}
		logBackendAnomaly(msg, keyInfo, backendName, model, statusCode, reqBody, respBuf, latency, nil)
	}
}

func (h *Handler) bufferResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, isOpenClaw, isHermes, isDowngraded bool, reqBody []byte) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("read response body: %v", err)
		return
	}

	// Sanitize upstream domains from error responses (status >= 400)
	if statusCode >= 400 {
		respBody = sanitize.RemoveUpstreamDomains(respBody)
	}

	c.Writer.Write(respBody)

	// Decompress gzip for parsing/logging; the raw bytes have already been forwarded above.
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if r, err2 := gzip.NewReader(bytes.NewReader(respBody)); err2 == nil {
			if decoded, err3 := io.ReadAll(r); err3 == nil {
				respBody = decoded
			}
		}
	}

	in, out, cacheRead, cacheWrite := usage.ParseTokens(respBody)
	sawUsage := in > 0 || out > 0 || cacheRead > 0 || cacheWrite > 0
	rawZero := !sawUsage
	if !sawUsage {
		in = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
		out = tokenest.EstimateString(tokenest.ExtractResponseText(respBody), tokenest.Default)
	}
	latency := time.Since(start)
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, in, out, cacheRead, cacheWrite, latency, isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"), c.ClientIP(), reqBody)

	if statusCode >= 400 || rawZero {
		msg := "backend zero tokens"
		if statusCode >= 400 {
			msg = "backend error response"
		}
		logBackendAnomaly(msg, keyInfo, backendName, model, statusCode, reqBody, respBody, latency, nil)
	}
}

// costUSD estimates cost based on token counts and model, including prompt
// cache tokens (cache_read at a discount, cache_write at a premium).
// If pricing map is provided, it uses glob-pattern matching (same as AWS channel).
// Falls back to built-in prices when no pattern matches.
func costUSD(model string, inputTokens, outputTokens, cacheRead, cacheWrite int, pricing map[string]config.ModelPricingEntry) float64 {
	if len(pricing) > 0 {
		p := resolvePricing(model, pricing)
		// Entries that configure only input/output derive cache prices from the
		// standard Anthropic ratios (read = 10% of input, write = 125%).
		if p.CacheRead == 0 && p.CacheWrite == 0 {
			p.CacheRead = p.Input * 0.1
			p.CacheWrite = p.Input * 1.25
		}
		return (float64(inputTokens)*p.Input + float64(outputTokens)*p.Output +
			float64(cacheRead)*p.CacheRead + float64(cacheWrite)*p.CacheWrite) / 1_000_000
	}

	// Built-in fallback prices (per 1M tokens); cache follows Anthropic's
	// standard ratios: read = 10% of input price, write = 125%.
	inputPrice := 3.0
	outputPrice := 15.0

	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude-haiku"):
		inputPrice, outputPrice = 1.0, 5.0
	case strings.Contains(m, "claude-opus"):
		inputPrice, outputPrice = 5.0, 25.0
	case strings.Contains(m, "claude-sonnet"):
		inputPrice, outputPrice = 3.0, 15.0
	case strings.Contains(m, "gpt-5.3-codex"):
		inputPrice, outputPrice = 1.75, 14.0
	case strings.Contains(m, "gpt-5.4"):
		inputPrice, outputPrice = 2.5, 15.0
	case strings.Contains(m, "gpt-4o"):
		inputPrice, outputPrice = 2.5, 10.0
	case strings.Contains(m, "gpt-4"):
		inputPrice, outputPrice = 30.0, 60.0
	case strings.Contains(m, "gpt-3.5"):
		inputPrice, outputPrice = 0.5, 1.5
	}

	return (float64(inputTokens)*inputPrice + float64(outputTokens)*outputPrice +
		float64(cacheRead)*inputPrice*0.1 + float64(cacheWrite)*inputPrice*1.25) / 1_000_000
}

// resolvePricing matches a model name against glob patterns in the pricing map.
func resolvePricing(model string, pricing map[string]config.ModelPricingEntry) config.ModelPricingEntry {
	m := strings.ToLower(model)
	bestPattern := ""
	var bestEntry config.ModelPricingEntry
	for pattern, entry := range pricing {
		if matched, _ := filepath.Match(pattern, m); matched {
			if len(pattern) > len(bestPattern) {
				bestPattern = pattern
				bestEntry = entry
			}
		}
	}
	if bestPattern != "" {
		return bestEntry
	}
	return config.ModelPricingEntry{Input: 3.0, Output: 15.0}
}

func (h *Handler) emitUsage(keyInfo interface{}, keyStr, backendName, model string, statusCode, inputTokens, outputTokens, cacheRead, cacheWrite int, latency time.Duration, isOpenClaw, isHermes, isDowngraded bool, userAgent, clientIP string, reqBody []byte) {
	if h.collector == nil || keyInfo == nil {
		return
	}
	info, ok := keyInfo.(*auth.KeyInfo)
	if !ok {
		return
	}

	total := inputTokens + outputTokens
	h.mu.RLock()
	pricing := map[string]config.ModelPricingEntry{}
	var analyzeCfg config.AnalyzeConfig
	if h.config != nil {
		pricing = h.config.BackendModelPricing
		analyzeCfg = h.config.Analyze
	}
	h.mu.RUnlock()

	// Feature 004: extract a compressed signal + request role for offline abuse
	// analysis. This runs AFTER the response has been written to the client (this
	// path is off the forward hot path) and only touches the in-memory reqBody, so
	// SC-001 (no measurable P99 impact) holds. Only successful requests are
	// analyzed (user constraint "logs only handle successful ones"), and the
	// analyzer's own Haiku calls are skipped to prevent self-recursion (FR, R6).
	//
	// OPTIMIZATION NOTE: ParseRequest + Extract on 6-9w token bodies (240-360KB JSON)
	// can consume ~5-10ms CPU per request. With actual usage at 0.0008% (5/611547),
	// consider disabling this feature if CPU spikes persist. The primary bottleneck
	// is SQLite batch-insert lock time, not classification.
	var signalJSON, requestRole string
	if analyzeCfg.Enabled && statusCode < 400 && len(reqBody) > 0 &&
		!(analyzeCfg.AnalyzerUA != "" && userAgent == analyzeCfg.AnalyzerUA) {
		if req, err := classify.ParseRequest(reqBody); err == nil {
			role := classify.RequestRole(req)
			requestRole = string(role)
			if role == classify.RoleUserInitiated {
				sig := classify.Extract(req, classify.FromAnalyzeConfig(analyzeCfg))
				if b, err := json.Marshal(sig); err == nil {
					signalJSON = string(b)
				}
			}
		}
	}
	// Calculate cost: only charge for successful requests (statusCode < 400)
	cost := 0.0
	if statusCode < 400 {
		cost = costUSD(model, inputTokens, outputTokens, cacheRead, cacheWrite, pricing)
	}

	ua := parseUA(userAgent, isOpenClaw, isHermes)
	// For DB is_openclaw field: both openclaw and hermes count as lobster traffic
	isLobster := isOpenClaw || isHermes
	// Tag with geolocation: Observe counts the request and returns the known
	// city (empty until check --ip2region resolves it) plus HQ classification.
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
		Backend:          backendName,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		TotalTokens:      total,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostUSD:          cost,
		StatusCode:       statusCode,
		Latency:          latency,
		IsOpenClaw:       isLobster,
		IsDowngraded:     isDowngraded,
		UA:               ua,
		ErrorReason:      reasonCode(ClassifyError(statusCode, nil), statusCode, false),
		IP:               clientIP,
		City:             city,
		IsHQ:             isHQ,
		SignalJSON:       signalJSON,
		RequestRole:      requestRole,
	})

	// Accumulate backend daily cost for per-user quota tracking (统一统计所有请求)
	if cost > 0 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
}

func logBackendAnomaly(msg string, keyInfo interface{}, backendName, model string, statusCode int, reqBody, respBody []byte, latency time.Duration, reqErr error) {
	fields := logrus.Fields{
		"backend":     backendName,
		"model":       model,
		"status_code": statusCode,
		"latency_ms":  latency.Milliseconds(),
	}
	if reqErr != nil {
		fields["error"] = reqErr.Error()
	}
	if info, ok := keyInfo.(*auth.KeyInfo); ok && info != nil {
		fields["itcode"] = info.Itcode
		fields["user_id"] = info.UserID
	}
	if len(reqBody) > 0 {
		body := string(reqBody)
		if len(body) > 4096 {
			body = body[:4096] + "...(truncated)"
		}
		fields["request_body"] = body
	}
	if len(respBody) > 0 {
		resp := string(respBody)
		if len(resp) > 4096 {
			resp = resp[:4096] + "...(truncated)"
		}
		fields["response_body"] = resp
	}
	logger.LogBackendRequest(msg, fields, "request_body", "response_body")
}

// ChatCompletions handles POST /v1/chat/completions (OpenAI style).
func (h *Handler) ChatCompletions(c *gin.Context) {
	h.forward(c, "/v1/chat/completions")
}

// Messages handles POST /v1/messages (Anthropic style).
func (h *Handler) Messages(c *gin.Context) {
	h.forward(c, "/v1/messages")
}

// Models handles GET /v1/models — fetches from upstream backend, filters to Claude models only,
// then appends public models (via extra_models context).
func (h *Handler) Models(c *gin.Context) {
	backend := h.lb.Pick()
	if backend == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available backend"})
		return
	}

	url := strings.TrimRight(backend.URL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create request failed"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+backend.APIKey)

	resp, err := backend.Client().Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "read upstream response failed"})
		return
	}

	var result struct {
		Data []gin.H `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "parse upstream response failed"})
		return
	}

	// Filter: keep only models whose id contains "claude"
	var filtered []gin.H
	for _, m := range result.Data {
		id, _ := m["id"].(string)
		if strings.Contains(strings.ToLower(id), "claude") {
			filtered = append(filtered, m)
		}
	}

	if extra, ok := c.Get("extra_models"); ok {
		if extraModels, ok := extra.([]gin.H); ok {
			filtered = append(filtered, extraModels...)
		}
	}

	c.JSON(http.StatusOK, gin.H{"object": "list", "data": filtered})
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

// UpdateConfig updates the handler config and rebuilds the web-search client
// when the websearch section changed (SIGHUP hot-reload).
func (h *Handler) UpdateConfig(cfg *config.Config) {
	h.mu.Lock()
	h.config = cfg
	if cfg != nil {
		h.webSearchCfg = cfg.WebSearch
		if cfg.WebSearch.Enabled && cfg.WebSearch.SearchURL != "" {
			h.searxng = websearch.NewClient(cfg.WebSearch)
		} else {
			h.searxng = nil
		}
	}
	h.mu.Unlock()
}

// ReplaceModelInBody rewrites the "model" field in a JSON request body from
// oldModel to newModel. Handles both "model":"value" and "model": "value" forms.
// It is idempotent: when oldModel == newModel (or oldModel is absent) the body
// is returned unchanged.
func ReplaceModelInBody(body []byte, oldModel, newModel string) []byte {
	body = bytes.Replace(body, []byte(`"model":"`+oldModel+`"`), []byte(`"model":"`+newModel+`"`), 1)
	body = bytes.Replace(body, []byte(`"model": "`+oldModel+`"`), []byte(`"model": "`+newModel+`"`), 1)
	return body
}
