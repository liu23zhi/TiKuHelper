package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func doDeviceAccessProbe(t *testing.T, db *gorm.DB, user *entity.User, deviceID string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/search/health", func(c *gin.Context) { c.Set("user", user); c.Next() }, DeviceAccessProbeWith(db),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) })
	req := httptest.NewRequest(http.MethodGet, "/search/health", nil)
	req.Header.Set(DeviceIDHeader, deviceID)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	return out
}

func seedProbeDeviceBlock(t *testing.T, db *gorm.DB, userID int32, kind string) {
	t.Helper()
	var err error
	if kind == "user-blocked" {
		err = db.Create(&entity.UserDeviceBlock{UserID: userID, DeviceID: "blocked-device"}).Error
	} else {
		err = db.Create(&entity.BlacklistEntry{Kind: "device", Pattern: "blocked-device", ValueHash: service.BlacklistPatternHashHex("blocked-device"), Enabled: true}).Error
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertDeviceAccessRejection(t *testing.T, out *httptest.ResponseRecorder, reason string) {
	t.Helper()
	var body struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
		Used   int    `json:"used"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusForbidden || body.Code != "DEVICE_LIMIT_EXCEEDED" || body.Reason != reason {
		t.Fatalf("device rejection must keep its specific reason: status=%d body=%s", out.Code, out.Body.String())
	}
}

func TestDeviceAccessProbeAndSearchRejectBlockedUnlimitedDevices(t *testing.T) {
	for _, kind := range []string{"user-blocked", "device-blacklisted"} {
		for _, enabled := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-switch-off", true: "-switch-on"}[enabled], func(t *testing.T) {
				db := newDeviceLimitTestDB(t)
				setDeviceLimitSwitch(t, db, enabled)
				level := seedDeviceLimitLevel(t, db, 1, 0)
				user := seedDeviceLimitUser(t, db, "blocked-unlimited", level, -1)
				seedProbeDeviceBlock(t, db, user.ID, kind)
				assertDeviceAccessRejection(t, doDeviceAccessProbe(t, db, user, "blocked-device"), kind)
				assertDeviceAccessRejection(t, doDeviceLimitRequest(t, db, deviceLimitTestRequest{user: user, deviceID: "blocked-device"}), kind)
				if out := doDeviceAccessProbe(t, db, user, "allowed-device"); out.Code != http.StatusOK {
					t.Fatalf("unblocked control: %d %s", out.Code, out.Body.String())
				}
			})
		}
	}
}

func TestDeviceAccessProbeRejectsCapacityWithoutRegisteringOrRefreshing(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	setDeviceLimitSwitch(t, db, true)
	level := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "capacity-probe", level, -1)
	seen := time.Date(2026, 11, 1, 2, 3, 4, 0, time.UTC)
	binding := entity.AccountDeviceBinding{UserID: user.ID, DeviceID: "existing-device", FirstSeenAt: seen, LastSeenAt: seen, LastIP: "172.18.0.1"}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	out := doDeviceAccessProbe(t, db, user, "new-device")
	assertDeviceAccessRejection(t, out, "limit-exceeded")
	var body map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["limit"] != float64(1) || body["used"] != float64(1) {
		t.Fatalf("capacity response must contain actual counts: %s", out.Body.String())
	}
	if out := doDeviceAccessProbe(t, db, user, "existing-device"); out.Code != http.StatusOK {
		t.Fatalf("existing device must remain allowed: %d %s", out.Code, out.Body.String())
	}
	var after entity.AccountDeviceBinding
	if err := db.First(&after, binding.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !after.LastSeenAt.Equal(seen) || after.LastIP != binding.LastIP {
		t.Fatalf("health must not refresh device activity: %+v", after)
	}
	if err := db.Table(entity.TableNameUserLevel).Where("id = ?", level).Update("device_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	if out := doDeviceAccessProbe(t, db, user, "new-device"); out.Code != http.StatusOK {
		t.Fatalf("available slot control: %d %s", out.Code, out.Body.String())
	}
	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("health must not register new devices: count=%d err=%v", count, err)
	}
}

func TestDeviceAccessProbePreservesHeadlessAndExistingDeviceCapacityExemptions(t *testing.T) {
	db := newDeviceLimitTestDB(t)
	level := seedDeviceLimitLevel(t, db, 1, 1)
	user := seedDeviceLimitUser(t, db, "probe-capacity-exemptions", level, -1)
	setDeviceLimitSwitch(t, db, false)
	assertDeviceLimitHeadlessRejected(t, doDeviceAccessProbe(t, db, user, ""), "legacy false cannot disable limited health probe")
	setDeviceLimitSwitch(t, db, true)
	assertDeviceLimitHeadlessRejected(t, doDeviceAccessProbe(t, db, user, ""), "enabled limited health probe")
	if err := db.Table(entity.TableNameUserLevel).Where("id = ?", level).Update("device_limit", 0).Error; err != nil {
		t.Fatal(err)
	}
	if out := doDeviceAccessProbe(t, db, user, ""); out.Code != http.StatusOK {
		t.Fatalf("unlimited headless health probe must remain usable: %d %s", out.Code, out.Body.String())
	}
	seedDeviceLimitBinding(t, db, user.ID, "existing-one")
	seedDeviceLimitBinding(t, db, user.ID, "existing-two")
	if err := db.Table(entity.TableNameUserLevel).Where("id = ?", level).Update("device_limit", 1).Error; err != nil {
		t.Fatal(err)
	}
	if out := doDeviceAccessProbe(t, db, user, "existing-one"); out.Code != http.StatusOK {
		t.Fatalf("lowering the limit must not eject existing devices: %d %s", out.Code, out.Body.String())
	}
	assertDeviceAccessRejection(t, doDeviceAccessProbe(t, db, user, "new-device"), "limit-exceeded")
}

func TestDeviceAccessProbeCapacityHintOnlyForEnforcedNonScriptRequests(t *testing.T) {
	for _, scenario := range []struct {
		name                  string
		enabled, script, hint bool
	}{
		{"enabled", true, false, true},
		{"legacy-disabled-ignored", false, false, true},
		{"script", true, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := newDeviceLimitTestDB(t)
			setDeviceLimitSwitch(t, db, scenario.enabled)
			level := seedDeviceLimitLevel(t, db, 1, 1)
			user := seedDeviceLimitUser(t, db, "hint-owner", level, -1)
			seedDeviceLimitBinding(t, db, user.ID, "existing-one")
			seedDeviceLimitBinding(t, db, user.ID, "existing-two")
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.GET("/search/health", func(c *gin.Context) {
				c.Set("user", user)
				if scenario.script {
					c.Set("script_session", struct{}{})
				}
				c.Next()
			}, DeviceAccessProbeWith(db), func(c *gin.Context) {
				_, exists := c.Get(DeviceAccessProbeContextKey)
				if exists != scenario.hint {
					t.Errorf("hint exists=%v want=%v", exists, scenario.hint)
				}
				c.JSON(http.StatusOK, gin.H{"success": true})
			})
			req := httptest.NewRequest(http.MethodGet, "/search/health", nil)
			req.Header.Set(DeviceIDHeader, "existing-one")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("existing device should remain allowed: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
