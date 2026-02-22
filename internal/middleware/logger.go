package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/claude-gateway/claude-gateway/internal/logger"
)

// RequestLogger logs each HTTP request with method, path, status, and latency.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		logger.WithFields(logrus.Fields{
			"method":  c.Request.Method,
			"path":    path,
			"status":  c.Writer.Status(),
			"latency": time.Since(start).Milliseconds(),
			"ip":      c.ClientIP(),
		}).Info("request")
	}
}
