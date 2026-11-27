package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// DeviceHostAdmission explicitly registers an ordinary native host before its
// first browser. It never invokes script-session auth or any search/billing path.
func DeviceHostAdmission(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if strings.TrimSpace(c.GetHeader("X-Script-Identity")) != "" || strings.TrimSpace(c.GetHeader("X-Script-Token")) != "" {
		c.JSON(http.StatusForbidden, gin.H{"message": "考试脚本凭据不能登记普通设备"})
		return
	}
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if authorization == "" {
		writeDeviceHostError(c, service.ErrInvalidAccessTokenAuthorization)
		return
	}
	db := deviceSessionDB(c)
	if db == nil {
		writeDeviceHostError(c, service.ErrDeviceHostUnavailable)
		return
	}
	auth, err := service.ResolveDeviceHostAccessToken(db, authorization)
	if err != nil {
		writeDeviceHostError(c, err)
		return
	}
	var input struct {
		DeviceID       string `json:"deviceId"`
		ClientInstance string `json:"clientInstance"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式错误"})
		return
	}
	deviceID, decision, err := service.AdmitDeviceHost(db, auth, input.DeviceID, input.ClientInstance, c.ClientIP())
	if err != nil {
		writeDeviceHostError(c, err)
		return
	}
	if !decision.Allowed {
		if decision.Reason == service.DevicePoolReasonDeviceIDConflict {
			WriteDeviceConflict(c)
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"code": "DEVICE_LIMIT_EXCEEDED", "reason": decision.Reason, "message": decision.Message,
			"limit": decision.Limit, "used": decision.Used})
		return
	}
	c.JSON(http.StatusOK, gin.H{"admitted": true, "deviceId": deviceID})
}

func writeDeviceHostError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidAccessTokenAuthorization):
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请提供有效普通访问令牌"})
	case errors.Is(err, service.ErrDeviceHostAccountBanned):
		c.JSON(http.StatusForbidden, gin.H{"code": "ACCOUNT_BANNED", "message": service.ErrDeviceHostAccountBanned.Error()})
	case errors.Is(err, service.ErrDeviceHostInvalidIdentity):
		c.JSON(http.StatusBadRequest, gin.H{"message": service.ErrDeviceHostInvalidIdentity.Error()})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": service.ErrDeviceHostUnavailable.Error()})
	}
}
