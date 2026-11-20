package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

const DeviceAccessProbeContextKey = "device_access_probe"

// DeviceAccessProbe validates the calling device without registering it or
// updating its activity. Status polling stays available for already-issued work.
func DeviceAccessProbe() gin.HandlerFunc { return DeviceAccessProbeWith(nil) }

func DeviceAccessProbeWith(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil {
			return
		}
		database := deviceLimitDatabase(db)
		if c.Request == nil || database == nil {
			c.Next()
			return
		}
		userID, scriptSession, _, ok := deviceLimitIdentity(c, database)
		if !ok {
			c.Next()
			return
		}
		deviceID := service.NormalizeDevicePoolDeviceID(c.GetHeader(DeviceIDHeader))
		if blocked := service.CheckDeviceBlockWithDB(database, userID, deviceID); !blocked.Allowed {
			deviceLimitReject(c, blocked)
			return
		}
		if !service.GetDeviceLimitEnabledWithDB(database) || scriptSession {
			c.Next()
			return
		}
		limit, _, err := service.EffectiveDeviceLimit(database, userID)
		if err != nil || limit == service.DevicePoolUnlimited {
			c.Next()
			return
		}
		if deviceID == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": DeviceIDRequiredCode, "message": DeviceIDRequiredMessage,
				"limit": limit, "reason": "device-id-required",
			})
			return
		}
		decision := service.InspectDeviceAccessWithDB(database, userID, deviceID)
		if !decision.Allowed {
			deviceLimitReject(c, decision)
			return
		}
		if decision.Reason == service.DevicePoolReasonAlreadyRegistered && decision.Limit > 0 && decision.Used > int(decision.Limit) {
			c.Set(DeviceAccessProbeContextKey, decision)
		}
		c.Next()
	}
}
