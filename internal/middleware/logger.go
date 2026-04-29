package middleware

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
)

// RequestLogger logs each HTTP request with method, path, status, and latency.
// For proxy endpoints it also logs itcode and backend name.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		fields := logrus.Fields{
			"method":  c.Request.Method,
			"path":    path,
			"status":  c.Writer.Status(),
			"latency": time.Since(start).Milliseconds(),
			"ip":      c.ClientIP(),
		}

		// Distinguish proxy (forward) requests from management API requests
		isProxy := strings.HasPrefix(path, "/v1/")
		if isProxy {
			fields["type"] = "forward"
			if backend, ok := c.Get("proxy_backend"); ok {
				fields["backend"] = backend
			}
			// Extract User-Agent product name (before /), max 12 chars
			if ua := c.GetHeader("User-Agent"); ua != "" {
				product := ua
				if idx := strings.Index(product, "/"); idx > 0 {
					product = product[:idx]
				}
				if len(product) > 12 {
					product = product[:12]
				}
				fields["ua"] = product
			}
			// First try to get itcode from authenticated KeyInfo
			if info, ok := c.Get(CtxKeyInfo); ok {
				if ki, ok := info.(*auth.KeyInfo); ok && ki.Itcode != "" {
					fields["itcode"] = ki.Itcode
				}
			}
			// If auth failed, at least show the key prefix for debugging
			if _, ok := c.Get(CtxKeyInfo); !ok {
				if keyPrefix, ok := c.Get("raw_api_key"); ok {
					fields["key"] = keyPrefix
				}
			}
			if _, ok := c.Get("is_openclaw"); ok {
				fields["openclaw"] = true
			}
			if _, ok := c.Get("is_hermes"); ok {
				fields["hermesclaw"] = true
			}
		} else {
			fields["type"] = "api"
		}

		logger.WithFields(fields).Info("request")
	}
}
