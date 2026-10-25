package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// SetHealthRouter 区分进程存活与数据库就绪，不调用收费上游。
// checkReadiness 必须响应 context；每次探测最多给数据库一秒预算。
func SetHealthRouter(router *gin.Engine, checkReadiness func(context.Context) error) {
	router.GET("/healthz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if checkReadiness == nil || ctx.Err() != nil || checkReadiness(ctx) != nil || ctx.Err() != nil {
			// 公开探针不返回数据库地址、凭据或内部错误详情。
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
}
