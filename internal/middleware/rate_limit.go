package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

func CORS(c *gin.Context) {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin != "" {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Credentials", "true")
	}

	c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.Next()
}

// BodyLimit limits request bodies to protect public endpoints from memory and disk exhaustion.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// SecurityHeaders applies conservative browser security headers.
func SecurityHeaders(c *gin.Context) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	c.Next()
}

// GlobalAPIRateLimit 全局API限流
func GlobalAPIRateLimit(c *gin.Context) {
	limiter := manager.GetManager().GetIPLimiter()
	congig := manager.GetManager().GetConfig()
	if congig.Limit.Enable && !limiter.GetLimiter(c.ClientIP()).Allow() {
		c.AbortWithStatus(http.StatusTooManyRequests)
	}
}
