package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// clearAIResponseWriteDeadline keeps a synchronous AI request from being cut
// off by the process-wide HTTP server WriteTimeout after the model starts
// streaming its response.
func clearAIResponseWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
}
