package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/extensionlogs"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var extensionLogFiles = extensionlogs.Default
var extensionLogOwner = func(authorization string) (int32, error) {
	auth, err := service.ResolveAccessTokenFromBasicAuth(manager.GetManager().GetDB(), authorization)
	return auth.User.ID, err
}

func readLogJSON(c *gin.Context, v any, size int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, size)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		err = dec.Decode(&struct{}{})
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "日志批次过大"})
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"message": "日志数据格式不正确"})
	return false
}
func UploadExtensionLogs(c *gin.Context) {
	header := c.GetHeader("Authorization")
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "需要有效的扩展访问令牌"})
		return
	}
	owner, err := extensionLogOwner(header)
	if err != nil || owner <= 0 {
		if err != nil && !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "暂时无法校验访问令牌"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"message": "访问令牌无效"})
		return
	}
	var batch extensionlogs.Batch
	if !readLogJSON(c, &batch, 64<<10) {
		return
	}
	if err = extensionLogFiles.Append(owner, batch); err != nil {
		if errors.Is(err, extensionlogs.ErrBatch) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "不支持的诊断事件、字段值或批次大小"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "诊断日志暂时无法写入"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"accepted": len(batch.Entries)})
}
func listExtensionLogsFor(c *gin.Context, owner int32) {
	n, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || n < 1 || n > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "条数范围为 1–100"})
		return
	}
	page, err := extensionLogFiles.Read(c.Query("cursor"), n, owner)
	if errors.Is(err, extensionlogs.ErrCursor) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无效的日志页码"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "暂时无法读取诊断日志"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, page)
}
func AdminExtensionLogs(c *gin.Context) { listExtensionLogsFor(c, 0) }
func GetExtensionLogPolicy(c *gin.Context) {
	policy, err := extensionLogFiles.Policy()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "暂时无法读取诊断日志策略"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, policy)
}
func SetExtensionLogPolicy(c *gin.Context) {
	var policy extensionlogs.Policy
	if !readLogJSON(c, &policy, 4096) {
		return
	}
	err := extensionLogFiles.SetPolicy(policy)
	if errors.Is(err, extensionlogs.ErrPolicy) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写 1–3650 天、1–1024 GB 的完整整数"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "日志策略未完全应用，请刷新后核实"})
		return
	}
	c.JSON(http.StatusOK, policy)
}
