package logger

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
)

const ScriptEntryLogKey = "http_log_script_entry"

// AccessLogger keeps route diagnostics while excluding all query credentials
// and public script addresses, which themselves can authorize a short launch.
func AccessLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		path := p.Request.URL.Path
		if script, _ := p.Keys[ScriptEntryLogKey].(bool); script {
			path = "[script-entry]"
		}
		path = strings.NewReplacer("\r", "", "\n", "").Replace(path)
		return fmt.Sprintf("[HTTP] %s | %3d | %13v | %15s | %-7s %q\n", p.TimeStamp.Format("2006/01/02 - 15:04:05"), p.StatusCode, p.Latency, p.ClientIP, p.Method, path)
	})
}

// Gin's default recovery dumps request headers and raw URLs. Keep the stack,
// but omit those credentials as well as arbitrary panic payloads.
func HTTPRecovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		SysError("HTTP request panic recovered\n" + string(debug.Stack()))
		c.AbortWithStatus(500)
	})
}
