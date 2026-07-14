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
	"github.com/wjzhangq/claude-gateway/internal/sanitize"
	"github.com/wjzhangq/claude-gateway/internal/stats"
	"github.com/wjzhangq/claude-gateway/internal/tokenest"
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
}

func NewHandler(lb *LoadBalancer, collector *stats.Collector, keyStore *auth.KeyStore, cfg *config.Config, modelReplacements map[string]string) *Handler {
	return &Handler{
		lb:                lb,
		collector:         collector,
		keyStore:          keyStore,
		config:            cfg,
		modelReplacements: modelReplacements,
		fallbackClient:    &http.Client{Timeout: 5 * time.Minute},
	}
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
		// Normal path with quota failover. If a backend returns an upstream
		// "insufficient balance" / account-suspended 429, circuit-break it and
		// retry the same request on a different backend. This converts the single
		// largest error class (exceeded_current_quota_error) into transparent
		// failover instead of a user-facing 429.
		tried := map[string]bool{backend.Name: true}
		failovers := 0
		// Bound the whole failover chain by a wall-clock budget (FR-022) so a
		// cascade cannot stack per-attempt latency into a ~45s tail.
		chainDeadline := start.Add(failoverBudgetSecs * time.Second)
		for {
			resp, err = h.doRequest(c, backend, upstreamPath, targetURL, body)

			// Client aborted the request (context canceled/deadline): this is not
			// a backend fault. Don't count it against backend health and don't log
			// it as an error — there is no client left to serve.
			if err != nil && IsClientCanceled(err) {
				backend.RecordResult(ErrCanceled)
				logger.Infof("client canceled request: backend=%s model=%s", backend.Name, reqModel)
				return
			}

			if err != nil {
				break // genuine transport error: handled by the block below
			}

			// Detect an upstream quota-exhaustion 429 and fail over to another
			// backend. The error body is tiny, so reading it fully is cheap.
			if resp.StatusCode == http.StatusTooManyRequests {
				peek, isQuota := peekQuotaExhausted(resp)
				if isQuota {
					// Release the abandoned backend's connection back to the pool
					// before we drop the response and retry elsewhere.
					resp.Body.Close()
					h.lb.MarkQuotaExhausted(backend.Name)
					failovers++
					next := h.lb.PickExcluding(tried)
					// Stop failing over when: no alternate, the per-request cap is
					// hit, the chain deadline elapsed (FR-022), or a quota cascade is
					// active — in a cascade, retrying just adds upstream load at the
					// worst moment, so fast-fail (FR-023).
					if next == nil || failovers > maxQuotaFailovers || time.Now().After(chainDeadline) || h.lb.QuotaCascadeActive() {
						resp.Body = io.NopCloser(bytes.NewReader(peek))
						logger.Warnf("quota failover stopped (backend=%s failovers=%d cascade=%v): returning 429", backend.Name, failovers, h.lb.QuotaCascadeActive())
						break // keep this response; recorded once below
					}
					// Abandon this backend: record its failure now, then retry.
					backend.RecordResult(ClassifyError(resp.StatusCode, nil))
					backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds())
					logger.Warnf("backend %s quota exhausted (insufficient balance): failing over to %s", backend.Name, next.Name)
					backend = next
					tried[backend.Name] = true
					targetURL = strings.TrimRight(backend.URL, "/") + upstreamPath
					continue
				}
				// Non-quota 429 (e.g. genuine rate limit): rebuild the body we
				// consumed so the downstream handler can forward it to the client.
				resp.Body = io.NopCloser(bytes.NewReader(peek))
			}

			// Detect a 403 that is a pre-charge quota failure (e.g. new-api
			// "预扣费额度失败 / Please run /login") rather than a bad API key.
			// These must not trigger ErrAuth (permanent disable) — instead
			// circuit-break via quotaExhausted and failover like a 429 quota error.
			if resp.StatusCode == http.StatusForbidden {
				peek, isLoginQuota := peekLoginQuotaError(resp)
				if isLoginQuota {
					resp.Body.Close()
					h.lb.MarkQuotaExhausted(backend.Name)
					failovers++
					next := h.lb.PickExcluding(tried)
					if next == nil || failovers > maxQuotaFailovers || time.Now().After(chainDeadline) || h.lb.QuotaCascadeActive() {
						resp.Body = io.NopCloser(bytes.NewReader(peek))
						logger.Warnf("login-quota failover stopped (backend=%s failovers=%d cascade=%v): returning 403", backend.Name, failovers, h.lb.QuotaCascadeActive())
						break
					}
					backend.RecordResult(ErrRateLimit) // counts against health but not auth-disable
					backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds())
					logger.Warnf("backend %s pre-charge quota failure (403): failing over to %s", backend.Name, next.Name)
					backend = next
					tried[backend.Name] = true
					targetURL = strings.TrimRight(backend.URL, "/") + upstreamPath
					continue
				}
				// Genuine 403 (bad key): restore body and let ErrAuth handle it.
				resp.Body = io.NopCloser(bytes.NewReader(peek))
			}
			break
		}

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
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, isDowngraded, body)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, isDowngraded, body)
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

// maxQuotaFailovers caps how many alternate backends a single request will try
// after hitting an upstream quota-exhaustion 429, bounding worst-case latency.
const maxQuotaFailovers = 2

// peekQuotaExhausted reads the (small) error body of a 429 response and reports
// whether it is an upstream account-suspended / insufficient-balance error
// (Anthropic/new-api type "exceeded_current_quota_error"). It returns the bytes
// it consumed so the caller can restore resp.Body. Detection is intentionally
// broad: it matches either the structured error type or the balance phrasing,
// so it survives minor upstream wording changes.
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

func peekQuotaExhausted(resp *http.Response) ([]byte, bool) {
	const maxPeek = 8192
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPeek))
	if err != nil {
		return body, false
	}
	s := strings.ToLower(string(body))
	if strings.Contains(s, "exceeded_current_quota_error") ||
		strings.Contains(s, "insufficient balance") ||
		strings.Contains(s, "suspended due to") {
		return body, true
	}
	return body, false
}

// peekLoginQuotaError reads the (small) body of a 403 response and reports
// whether it is a pre-charge quota failure from new-api style backends
// (e.g. "预扣费额度失败" / "Please run /login"). These look like auth errors
// by status code but are actually transient balance issues, so they must not
// trigger the permanent ErrAuth disable path.
func peekLoginQuotaError(resp *http.Response) ([]byte, bool) {
	const maxPeek = 8192
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPeek))
	if err != nil {
		return body, false
	}
	s := string(body)
	if strings.Contains(s, "预扣费") ||
		strings.Contains(s, "/login") ||
		strings.Contains(s, "API Error: 403") ||
		strings.Contains(s, "用户剩余额度") {
		return body, true
	}
	return body, false
}

const streamReadBufSize = 4096

func (h *Handler) streamResponse(c *gin.Context, resp *http.Response, backendName, model string, keyInfo interface{}, keyStr string, statusCode int, start time.Time, isOpenClaw, isHermes, isDowngraded bool, reqBody []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)
	ctx := c.Request.Context()
	var lastIn, lastOut int
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
			c.Writer.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
			respBuf = append(respBuf, buf[:n]...)
			// Parse SSE lines incrementally for token counting
			partial = append(partial, buf[:n]...)
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
				if i, o := parseBodyTokens(payload); i > 0 || o > 0 {
					lastIn, lastOut = i, o
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

	if lastIn == 0 {
		lastIn = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
	}
	if lastOut == 0 {
		lastOut = outCounts.Estimate(tokenest.Default)
	}
	// rawZero after fallback: a genuinely empty stream (no request text, no
	// streamed delta content). This avoids flagging truncated-but-successful
	// streams — where usage tokens never arrived but real deltas did — as
	// "backend zero tokens".
	rawZero := lastIn == 0 && lastOut == 0

	latency := time.Since(start)
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, lastIn, lastOut, latency, isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"), c.ClientIP(), reqBody)

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
	c.Writer.Write(respBody)

	// Decompress gzip for parsing/logging; the raw bytes have already been forwarded above.
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if r, err2 := gzip.NewReader(bytes.NewReader(respBody)); err2 == nil {
			if decoded, err3 := io.ReadAll(r); err3 == nil {
				respBody = decoded
			}
		}
	}

	in, out := parseBodyTokens(respBody)
	rawZero := in == 0 && out == 0
	if in == 0 {
		in = tokenest.EstimateString(tokenest.ExtractRequestText(reqBody), tokenest.Default)
	}
	if out == 0 {
		out = tokenest.EstimateString(tokenest.ExtractResponseText(respBody), tokenest.Default)
	}
	latency := time.Since(start)
	h.emitUsage(keyInfo, keyStr, backendName, model, statusCode, in, out, latency, isOpenClaw, isHermes, isDowngraded, c.Request.Header.Get("User-Agent"), c.ClientIP(), reqBody)

	if statusCode >= 400 || rawZero {
		msg := "backend zero tokens"
		if statusCode >= 400 {
			msg = "backend error response"
		}
		logBackendAnomaly(msg, keyInfo, backendName, model, statusCode, reqBody, respBody, latency, nil)
	}
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

// costUSD estimates cost based on token counts and model.
// If pricing map is provided, it uses glob-pattern matching (same as AWS channel).
// Falls back to built-in prices when no pattern matches.
func costUSD(model string, inputTokens, outputTokens int, pricing map[string]config.ModelPricingEntry) float64 {
	if len(pricing) > 0 {
		p := resolvePricing(model, pricing)
		return (float64(inputTokens)*p.Input + float64(outputTokens)*p.Output) / 1_000_000
	}

	// Built-in fallback prices (per 1M tokens)
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

	return (float64(inputTokens)*inputPrice + float64(outputTokens)*outputPrice) / 1_000_000
}

// resolvePricing matches a model name against glob patterns in the pricing map.
func resolvePricing(model string, pricing map[string]config.ModelPricingEntry) config.ModelPricingEntry {
	m := strings.ToLower(model)
	for pattern, entry := range pricing {
		if matched, _ := filepath.Match(pattern, m); matched {
			return entry
		}
	}
	// default fallback
	return config.ModelPricingEntry{Input: 3.0, Output: 15.0}
}

func (h *Handler) emitUsage(keyInfo interface{}, keyStr, backendName, model string, statusCode, inputTokens, outputTokens int, latency time.Duration, isOpenClaw, isHermes, isDowngraded bool, userAgent, clientIP string, reqBody []byte) {
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
	cost := costUSD(model, inputTokens, outputTokens, pricing)
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
		ErrorReason:  reasonCode(ClassifyError(statusCode, nil), statusCode, false),
		IP:           clientIP,
		City:         city,
		IsHQ:         isHQ,
		SignalJSON:   signalJSON,
		RequestRole:  requestRole,
	})

	// Accumulate backend daily cost for per-user quota tracking
	if cost > 0 && statusCode < 400 {
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

// UpdateConfig updates the handler config.
func (h *Handler) UpdateConfig(cfg *config.Config) {
	h.mu.Lock()
	h.config = cfg
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
