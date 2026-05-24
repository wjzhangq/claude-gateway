package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// RequestLogger logs each HTTP request.
// For /v1/* proxy requests, outputs a concise text line:
//   /v1/messages 200 claude-code/1.0 sk-abc1 wjzhang backend:lb-1 claude-sonnet-4 1234 5678
// For other requests, uses structured JSON logging.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		isProxy := strings.HasPrefix(path, "/v1/")
		if isProxy {
			logProxyRequest(c, path, start)
		} else {
			logAPIRequest(c, path, start)
		}
	}
}

func logProxyRequest(c *gin.Context, path string, start time.Time) {
	status := c.Writer.Status()

	// UA: product/version or product (max 20 chars)
	ua := c.GetHeader("User-Agent")
	if ua == "" {
		ua = "unknown"
	} else {
		if idx := strings.Index(ua, " "); idx > 0 {
			ua = ua[:idx]
		}
		if len(ua) > 20 {
			ua = ua[:20]
		}
	}

	// Key prefix (first 8 chars)
	keyPrefix := "-"
	if rawKey, ok := c.Get("raw_api_key"); ok {
		if s, ok := rawKey.(string); ok && len(s) > 0 {
			if len(s) > 8 {
				keyPrefix = s[:8]
			} else {
				keyPrefix = s
			}
		}
	}

	// Itcode
	itcode := "-"
	if info, ok := c.Get(CtxKeyInfo); ok {
		if ki, ok := info.(*auth.KeyInfo); ok && ki.Itcode != "" {
			itcode = ki.Itcode
		}
	}

	// Provider:BackendName
	providerStr := "-"
	if provider, ok := c.Get("log_provider"); ok {
		if p, ok := provider.(string); ok && p != "" {
			providerStr = p
			if backendName, ok := c.Get("log_backend_name"); ok {
				if bn, ok := backendName.(string); ok && bn != "" {
					providerStr = p + ":" + bn
				}
			}
		}
	} else if backend, ok := c.Get("proxy_backend"); ok {
		if b, ok := backend.(string); ok && b != "" {
			providerStr = "backend:" + b
		}
	}

	// Model
	model := "-"
	if m, ok := c.Get("log_model"); ok {
		if s, ok := m.(string); ok && s != "" {
			model = s
		}
	}

	// Tokens
	inputTokens := 0
	outputTokens := 0
	if v, ok := c.Get("log_input_tokens"); ok {
		if n, ok := v.(int); ok {
			inputTokens = n
		}
	}
	if v, ok := c.Get("log_output_tokens"); ok {
		if n, ok := v.(int); ok {
			outputTokens = n
		}
	}

	line := fmt.Sprintf("%s %d %s %s %s %s %s %d %d",
		path, status, ua, keyPrefix, itcode, providerStr, model, inputTokens, outputTokens)
	logger.Info(line)
}

func logAPIRequest(c *gin.Context, path string, start time.Time) {
	_ = start
	// Non-proxy requests: minimal structured log
	logger.Infof("%s %s %d %dms", c.Request.Method, path, c.Writer.Status(), time.Since(start).Milliseconds())
}
