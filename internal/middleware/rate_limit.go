package middleware

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

func CORS(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin != "" && corsOriginAllowed(c, origin) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Credentials", "true")
	}

	c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

	if c.Request.Method == http.MethodOptions {
		if origin != "" && !corsOriginAllowed(c, origin) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	c.Next()
}

func corsOriginAllowed(c *gin.Context, origin string) bool {
	trimmedOrigin := strings.TrimSpace(origin)
	if trimmedOrigin == "" {
		return false
	}
	if sameOrigin(c, trimmedOrigin) {
		return true
	}
	allowedOrigins := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if allowedOrigins == "" {
		return true
	}
	for _, allowed := range strings.Split(allowedOrigins, ",") {
		if corsOriginMatchesPattern(trimmedOrigin, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func corsOriginMatchesPattern(origin string, pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.EqualFold(pattern, origin) {
		return true
	}

	patternURL, err := url.Parse(pattern)
	if err != nil || patternURL.Scheme == "" || patternURL.Hostname() == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(patternURL.Scheme, originURL.Scheme) {
		return false
	}
	if patternURL.Port() != "" && patternURL.Port() != originURL.Port() {
		return false
	}

	patternHost := strings.ToLower(patternURL.Hostname())
	originHost := strings.ToLower(originURL.Hostname())
	if !strings.HasPrefix(patternHost, "*.") {
		return patternHost == originHost
	}

	suffix := patternHost[1:]
	return strings.HasSuffix(originHost, suffix) && originHost != strings.TrimPrefix(suffix, ".")
}

func sameOrigin(c *gin.Context, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || c == nil || c.Request == nil {
		return false
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, c.Request.Host)
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
