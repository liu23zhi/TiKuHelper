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
	"gorm.io/gorm"
)

var extensionLogFiles = extensionlogs.Default
var diagnosticDB = func() *gorm.DB {
	m := manager.GetManager()
	if m == nil {
		return nil
	}
	return m.GetDB()
}
var extensionLogOwner = func(authorization string) (int32, error) {
	db := diagnosticDB()
	if db == nil {
		return 0, errors.New("diagnostic database unavailable")
	}
	auth, err := service.ResolveAccessTokenFromBasicAuth(db, authorization)
	return auth.User.ID, err
}
var examLogOwner = func(authorization, identity, ip string) (int32, error) {
	db := diagnosticDB()
	if db == nil {
		return 0, errors.New("diagnostic database unavailable")
	}
	return service.ResolveLeasedDiagnosticOwner(db, authorization, identity, ip)
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
func diagnosticBearer(c *gin.Context) (string, bool) {
	header := c.GetHeader("Authorization")
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "需要有效的诊断回传凭据"})
		return "", false
	}
	return header, true
}

func diagnosticOwner(c *gin.Context, header, mode string) (int32, bool) {
	mode = extensionlogs.EffectiveMode(mode)
	if !extensionlogs.KnownMode(mode) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无效的诊断模式"})
		return 0, false
	}
	identity := strings.TrimSpace(c.GetHeader("X-Script-Identity"))
	var id int32
	var err error
	switch {
	case mode == extensionlogs.ModeExam && identity != "":
		id, err = examLogOwner(header, identity, c.ClientIP())
	case mode != extensionlogs.ModeExam && identity == "":
		id, err = extensionLogOwner(header)
	default:
		err = service.ErrInvalidAccessTokenAuthorization // neither cross-family fallback nor self-declared grants
	}
	if err != nil || id <= 0 {
		if err != nil && !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"message": "暂时无法校验诊断回传凭据"})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "诊断回传凭据无效"})
		}
		return 0, false
	}
	return id, true
}
func GetExtensionIngestPolicy(c *gin.Context) {
	header, ok := diagnosticBearer(c)
	if !ok {
		return
	}
	if _, ok := diagnosticOwner(c, header, c.Query("mode")); !ok {
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
	header, ok := diagnosticBearer(c)
	if !ok {
		return
	}
	var session extensionlogs.Session
	if !readLogJSON(c, &session, 4096) {
		return
	}
	owner, ok := diagnosticOwner(c, header, session.Mode)
	if !ok {
		return
	}
	// created reports a new launch registration, not a diagnostic file.
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
	header, ok := diagnosticBearer(c)
	if !ok {
		return
	}
	var b extensionlogs.Batch
	if !readLogJSON(c, &b, 64<<10) {
		return
	}
	owner, ok := diagnosticOwner(c, header, b.Mode)
	if !ok {
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
	filter := extensionlogs.Filter{Mode: c.Query("mode"), BrowserID: c.Query("browser_id"), DeviceID: c.Query("device_id"), LaunchID: c.Query("launch_id")}
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
	policy.RetentionDays, policy.MaxGB = min(policy.RetentionDays, 7), min(policy.MaxGB, 10)
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
	if input.RetentionDays < 1 || input.RetentionDays > 7 || input.MaxGB < 1 || input.MaxGB > 10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写 1–7 天、1–10 GB 的完整整数"})
		return
	}
	policy := extensionlogs.Policy{Enabled: *input.Enabled, RetentionDays: input.RetentionDays, MaxGB: input.MaxGB}
	err := extensionLogFiles.SetPolicy(policy)
	if errors.Is(err, extensionlogs.ErrPolicy) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写 1–7 天、1–10 GB 的完整整数"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "日志策略未完全应用，请刷新后核实"})
		return
	}
	c.JSON(http.StatusOK, policy)
}
