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
			if info, ok := c.Get(CtxKeyInfo); ok {
				if ki, ok := info.(*auth.KeyInfo); ok && ki.Itcode != "" {
					fields["itcode"] = ki.Itcode
				}
			}
		} else {
			fields["type"] = "api"
		}

		logger.WithFields(fields).Info("request")
	}
}
