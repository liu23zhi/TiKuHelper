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
func diagnosticOwner(c *gin.Context) (int32, bool) {
	header := c.GetHeader("Authorization")
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "需要有效的扩展访问令牌"})
		return 0, false
	}
	id, err := extensionLogOwner(header)
	if err != nil || id <= 0 {
		if err != nil && !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "暂时无法校验访问令牌"})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "访问令牌无效"})
		}
		return 0, false
	}
	return id, true
}
func GetExtensionIngestPolicy(c *gin.Context) {
	if _, ok := diagnosticOwner(c); !ok {
		return
	}
	p, err := extensionLogFiles.Policy()
	if err != nil {
		c.JSON(503, gin.H{"message": "暂时无法读取回传策略"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"enabled": p.Enabled})
}
func diagnosticWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, extensionlogs.ErrDisabled):
		c.JSON(http.StatusForbidden, gin.H{"enabled": false, "message": "管理员已关闭扩展日志回传"})
	case errors.Is(err, extensionlogs.ErrLaunch):
		c.JSON(http.StatusConflict, gin.H{"need_start": true, "message": "请先发送本次启动信号"})
	case errors.Is(err, extensionlogs.ErrBatch):
		c.JSON(http.StatusBadRequest, gin.H{"message": "不支持的诊断事件、设备、启动标识或批次大小"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "诊断日志暂时无法写入"})
	}
}
func StartExtensionLogs(c *gin.Context) {
	owner, ok := diagnosticOwner(c)
	if !ok {
		return
	}
	var session extensionlogs.Session
	if !readLogJSON(c, &session, 4096) {
		return
	}
	created, err := extensionLogFiles.Start(owner, session)
	if err != nil {
		diagnosticWriteError(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"created": created})
}
func UploadExtensionLogs(c *gin.Context) {
	owner, ok := diagnosticOwner(c)
	if !ok {
		return
	}
	var b extensionlogs.Batch
	if !readLogJSON(c, &b, 64<<10) {
		return
	}
	if err := extensionLogFiles.Append(owner, b); err != nil {
		diagnosticWriteError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"accepted": len(b.Entries)})
}
func AdminExtensionLogs(c *gin.Context) {
	n, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || n < 1 || n > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "条数范围为 1–100"})
		return
	}
	filter := extensionlogs.Filter{DeviceID: c.Query("device_id"), LaunchID: c.Query("launch_id")}
	if raw, exists := c.GetQuery("user_id"); exists {
		v, e := strconv.ParseInt(raw, 10, 32)
		if e != nil || v <= 0 {
			c.JSON(400, gin.H{"message": "账号 ID 无效"})
			return
		}
		filter.UserID = int32(v)
	}
	page, err := extensionLogFiles.Read(c.Query("cursor"), n, filter)
	if errors.Is(err, extensionlogs.ErrCursor) {
		c.JSON(400, gin.H{"message": "无效的筛选条件或日志游标"})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"message": "暂时无法读取诊断日志"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, page)
}
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
	var input struct {
		Enabled       *bool `json:"enabled"`
		RetentionDays int   `json:"retention_days"`
		MaxGB         int   `json:"max_gb"`
	}
	if !readLogJSON(c, &input, 4096) {
		return
	}
	if input.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请明确扩展日志回传开关"})
		return
	}
	policy := extensionlogs.Policy{Enabled: *input.Enabled, RetentionDays: input.RetentionDays, MaxGB: input.MaxGB}
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
