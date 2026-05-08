package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

func CORS(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin != "" {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
	}

	requestHeaders := c.GetHeader("Access-Control-Request-Headers")
	if requestHeaders == "" {
		requestHeaders = "Authorization, Content-Type"
	}
	c.Header("Access-Control-Allow-Headers", requestHeaders)
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.Next()
}

// GlobalAPIRateLimit 全局API限流
func GlobalAPIRateLimit(c *gin.Context) {
	limiter := manager.GetManager().GetIPLimiter()
	congig := manager.GetManager().GetConfig()
	if congig.Limit.Enable && !limiter.GetLimiter(c.RemoteIP()).Allow() {
		c.AbortWithStatus(http.StatusTooManyRequests)
	}
}
