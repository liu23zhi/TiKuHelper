package controller

import (
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const platformRootKey = "platform_host_root"

// ScriptHostBoundary is installed before registering any Gin routes. Matched
// platform routes and the SPA fallback must obey the same authority decision.
func ScriptHostBoundary(db *gorm.DB) gin.HandlerFunc {
	return scriptHostBoundary(db, servePublicScriptBootstrap)
}
func scriptHostBoundary(db *gorm.DB, entry func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// A gateway itself is a loopback peer. Exempt only the existing public
		// liveness/readiness probes, never other site/API routes on that basis.
		if (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) && (c.Request.URL.Path == "/readyz" || c.Request.URL.Path == "/healthz") && service.IsLocalSiteAuthority(c.Request.Host) {
			peer, _, err := net.SplitHostPort(c.Request.RemoteAddr)
			if err == nil && net.ParseIP(peer).IsLoopback() {
				c.Next()
				return
			}
		}
		role, err := service.ScriptWebHostRoleWithDB(db, c.Request.Host)
		reject := func() { c.Header("Cache-Control", "no-store"); c.AbortWithStatus(http.StatusNotFound) }
		if err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		// Preserve registered HEAD routes and require a verified HTTPS scheme. No
		// request to this probe can enter the GET launcher or issue credentials.
		if role != service.RejectScriptWebHost && c.Request.Method == http.MethodHead && c.FullPath() == "" &&
			(c.Request.TLS != nil || c.GetBool(scriptProxyHTTPSKey)) &&
			c.Request.URL.RawPath == "" && c.Request.URL.RawQuery == "" && !c.Request.URL.ForceQuery &&
			service.ScriptAddressAvailableWithDB(db, c.Request.Host, c.Request.URL.Path) {
			c.Set(logger.ScriptEntryLogKey, true)
			c.Header("Cache-Control", "no-store")
			c.Header("Referrer-Policy", "no-referrer")
			c.Header("X-Tiku-Script-Address", "available")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		switch role {
		case service.PlatformWebHost:
			c.Set(platformRootKey, true)
			c.Next()
			return
		case service.StandaloneWebHost:
			c.Next()
			return
		case service.RejectScriptWebHost:
			reject()
			return
		}
		// This decision only restricts which EXISTING handler may be reached. The
		// subsequent handler still runs its usual credential checks and accounting.
		if c.Request.Method == http.MethodOptions {
			// First-party browser probes may preflight search/diagnostic requests. OPTIONS
			// carries no bearer: give only the ordinary CORS metadata on a recorded host
			// and named method. The actual request must still pass session ownership.
			if c.GetHeader("Origin") != "" && leaseAPIMethod(c.Request.URL.Path) == c.GetHeader("Access-Control-Request-Method") && c.GetHeader("Access-Control-Request-Method") != "" {
				ok, e := service.ScriptWebHostHasAssignmentWithDB(db, c.Request.Host)
				if e != nil {
					c.AbortWithStatus(http.StatusServiceUnavailable)
					return
				}
				if ok {
					middleware.CORS(c)
					return
				}
			}
			reject()
			return
		}
		route := c.FullPath()
		if method := leaseAPIMethod(route); method != "" && c.Request.Method == method {
			if service.ScriptSessionOwnsWebHostWithDB(db, c.Request.Host, c.GetHeader("Authorization"), c.GetHeader("X-Script-Identity"), c.ClientIP()) {
				c.Next()
				return
			}
			reject()
			return
		}
		switch route {
		case scriptLaunchResolvePath:
			if c.Request.Method == http.MethodPost {
				c.Next()
				return
			}
		case "/api/script-command/:shortCode/exchange", "/api/script-command/:shortCode/bootstrap", "/api/script-command/:shortCode/exchange-script", "/api/script-command/:shortCode/activate", "/api/script-command/:shortCode/heartbeat", "/api/script-command/:shortCode/file", "/api/scripts/:shortCode/bootstrap", "/api/scripts/command/:shortCode/exchange":
			ok, e := service.ScriptCodeOwnsWebHostWithDB(db, c.Request.Host, c.Param("shortCode"))
			if e != nil {
				c.AbortWithStatus(http.StatusServiceUnavailable)
				return
			}
			if ok {
				c.Next()
				return
			}
		case "", "/":
			if c.Request.Method == http.MethodGet && entry(c) {
				c.Abort()
				return
			}
		}
		reject()
	}
}

// The browser-facing, previously shared endpoints supported by a verified
// dedicated session. The normal site's generic API is not available here.
func leaseAPIMethod(path string) string {
	switch path {
	case "/api/search", "/adapter-service/search", "/api/search/tasks", "/adapter-service/search/tasks", "/api/extension-logs", "/api/extension-logs/start":
		return http.MethodPost
	case "/api/search/health", "/adapter-service/search/health", "/api/search/status", "/adapter-service/search/status", "/api/extension-logs/policy":
		return http.MethodGet
	}
	return ""
}
