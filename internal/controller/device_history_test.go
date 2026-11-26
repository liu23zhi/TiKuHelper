package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestDeviceHistoryAccountBlockRevokesOnlyOwnBindingAndPreventsRebind(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "history-owner", 0, -1)
	other := seedDeviceUserUser(t, db, "history-other", 0, -1)
	now := time.Now().UTC().Truncate(time.Second)
	id := seedDeviceUserBinding(t, db, owner.ID, "extension:history-shared", "", now, "", nil)
	otherID := seedDeviceUserBinding(t, db, other.ID, "extension:history-shared", "", now, "", nil)

	r := deviceUserRouter(owner)
	path := fmt.Sprintf("/api/user/devices/%d/block", id)
	for _, reason := range []string{"fixture reason", "duplicate must not replace"} {
		response, _ := doDeviceUser(t, r, http.MethodPost, path, map[string]any{"reason": reason})
		if response.Code != http.StatusOK {
			t.Fatalf("block: %d %s", response.Code, response.Body)
		}
	}
	for _, bindingID := range []int64{id, otherID} {
		var row entity.AccountDeviceBinding
		if err := db.First(&row, bindingID).Error; err != nil {
			t.Fatal(err)
		}
		if (row.RevokedAt != nil) != (bindingID == id) {
			t.Fatalf("wrong binding state: id=%d revokedAt=%v", bindingID, row.RevokedAt)
		}
	}
	if used, err := service.CountDevices(db, owner.ID); err != nil || used != 0 {
		t.Fatalf("account block retains slots: used=%d err=%v", used, err)
	}
	if decision, err := service.RegisterDevice(db, owner.ID, "extension:history-shared"); err != nil || decision.Allowed {
		t.Fatalf("blocked account rebind: %+v %v", decision, err)
	}
}

func TestDeviceHistoryDeleteIsIdempotentAndKeepsExistingBindingEvidence(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "history-delete", 0, -1)
	now := time.Now().UTC().Truncate(time.Second)
	id := seedDeviceUserBinding(t, db, owner.ID, "extension:history-clear", "", now, "user_removed", &now)
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("id = ?", id).
		Updates(map[string]any{"first_ip": "192.0.2.14", "last_ip": "198.51.100.14"}).Error; err != nil {
		t.Fatal(err)
	}
	r := deviceUserRouter(owner)
	for i := 0; i < 2; i++ {
		result, _ := doDeviceUser(t, r, http.MethodDelete, fmt.Sprintf("/api/user/devices/%d", id), nil)
		if result.Code != http.StatusOK {
			t.Fatalf("delete %d: %d %s", i, result.Code, result.Body)
		}
	}
	_, response := doDeviceUser(t, r, http.MethodGet, "/api/user/devices", nil)
	if rows := deviceUserItems(t, response); len(rows) != 0 {
		t.Fatalf("hidden history appears: %+v", rows)
	}
	var retained entity.AccountDeviceBinding
	if err := db.First(&retained, id).Error; err != nil || retained.RevokedAt == nil || retained.RevokedReason != "user_removed" || retained.LastIP != "198.51.100.14" {
		t.Fatalf("lost original removal evidence: %+v %v", retained, err)
	}
	if _, err := service.ResolveOwnLocationIPsWithDB(db, *owner, []string{"192.0.2.14"}); err != service.ErrLocationForbidden {
		t.Fatalf("hidden display IP still grants device-only lookup: %v", err)
	}
	if decision, err := service.RegisterDeviceWithOptions(db, owner.ID, retained.DeviceID, service.DeviceBindingRegisterOptions{ClientIP: "198.51.100.15"}); err != nil || !decision.Allowed {
		t.Fatalf("non-blocked device cannot rebind: %+v %v", decision, err)
	}
	_, response = doDeviceUser(t, r, http.MethodGet, "/api/user/devices", nil)
	rows := deviceUserItems(t, response)
	if len(rows) != 1 || rows[0]["id"] != float64(id) || rows[0]["status"] != "active" ||
		rows[0]["firstIp"] != "192.0.2.14" || rows[0]["lastIp"] != "198.51.100.15" || rows[0]["firstSeenAt"] != now.Format(time.RFC3339) {
		t.Fatalf("rebind should re-show accurate history: %+v", rows)
	}
}

func TestDeviceHistoryQuotaAndSessionReportingRejectScriptCredentials(t *testing.T) {
	owner := seedDeviceUserUser(t, newDeviceUserTestDB(t, true), "history-script", 0, -1)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user", owner); c.Set("script_session", struct{}{}) })
	router.GET("/quota", DeviceQuota)
	router.POST("/sessions", ReportDeviceSessions)
	for _, check := range []struct{ method, path string }{{http.MethodGet, "/quota"}, {http.MethodPost, "/sessions"}} {
		result, _ := doDeviceUser(t, router, check.method, check.path, map[string]any{"deviceId": "extension:ordinary", "count": 3})
		if result.Code != http.StatusForbidden {
			t.Fatalf("%s disclosed/accepted ordinary device data: %d %s", check.path, result.Code, result.Body)
		}
	}
}

func TestDeviceHistoryAdminFilteredSummaryAndLastIP(t *testing.T) {
	db := newDeviceAdminControllerTestDB(t, true)
	if err := db.AutoMigrate(&entity.UserDeviceBlock{}); err != nil {
		t.Fatal(err)
	}
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 612, "history-admin")
	active := seedDeviceAdminBinding(t, db, actor.ID, "extension:history-active")
	removed := seedDeviceAdminBinding(t, db, actor.ID, "extension:history-removed")
	blocked := seedDeviceAdminBinding(t, db, actor.ID, "extension:history-blocked")
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("id IN ?", []int64{removed.ID, blocked.ID}).
		Updates(map[string]any{"revoked_at": time.Now(), "last_ip": "192.168.1.41"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.UserDeviceBlock{UserID: actor.ID, DeviceID: blocked.DeviceID, Reason: "private reason"}).Error; err != nil {
		t.Fatal(err)
	}
	result := deviceAdminRequest(deviceAdminTestRouter(&actor), http.MethodGet,
		fmt.Sprintf("/api/admin/devices?userId=%d&status=revoked&pageSize=1", actor.ID), "")
	var response struct {
		Data []struct {
			ID     int64
			LastIP string `json:"lastIp"`
		}
		Total, Active, Removed, Blocked int
	}
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if result.Code != http.StatusOK || response.Total != 1 || response.Active != 0 || response.Removed != 1 || response.Blocked != 0 ||
		len(response.Data) != 1 || response.Data[0].ID != removed.ID || response.Data[0].LastIP != "192.168.1.41" {
		t.Fatalf("filtered summary/list disagree: active=%d removed=%d blocked=%d response=%s", active.ID, removed.ID, blocked.ID, result.Body)
	}
}

func TestDeviceHistoryConcurrentClearAndBlockHaveOneCoherentResult(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "history-concurrent", 0, -1)
	r := deviceUserRouter(owner)
	now := time.Now()
	const count = 8
	for i := 0; i < count; i++ {
		device := fmt.Sprintf("extension:history-race-%d", i)
		id := seedDeviceUserBinding(t, db, owner.ID, device, "", now, "user_removed", &now)
		path := fmt.Sprintf("/api/user/devices/%d", id)
		start := make(chan struct{})
		clear := make(chan int, 1)
		block := make(chan int, 1)
		go func() { <-start; clear <- deviceAdminRequest(r, http.MethodDelete, path, "").Code }()
		go func() { <-start; block <- deviceAdminRequest(r, http.MethodPost, path+"/block", "").Code }()
		close(start)
		clearCode, blockCode := <-clear, <-block
		var row entity.AccountDeviceBinding
		if err := db.First(&row, id).Error; err != nil {
			t.Fatal(err)
		}
		personal, err := service.AccountDeviceHasBlock(db, owner.ID, device)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case clearCode == http.StatusOK && blockCode == http.StatusNotFound:
			if row.HistoryHiddenAt == nil || personal {
				t.Fatalf("clear winner: wrong flags %+v block=%v", row, personal)
			}
		case clearCode == http.StatusConflict && blockCode == http.StatusOK:
			if row.HistoryHiddenAt != nil || !personal {
				t.Fatalf("block winner: wrong flags %+v block=%v", row, personal)
			}
		default:
			t.Fatalf("clear=%d block=%d", clearCode, blockCode)
		}
		if row.RevokedAt == nil || !row.RevokedAt.Equal(now) || row.RevokedReason != "user_removed" {
			t.Fatalf("concurrent actions rewrote removal evidence: %+v", row)
		}
	}
	var audits int64
	if err := db.Model(&entity.Log{}).Where("user_id = ? AND module = ?", owner.ID, "device").Count(&audits).Error; err != nil || audits != count {
		t.Fatalf("each state transition has exactly one audit, got %d err=%v", audits, err)
	}
}

func TestDeviceHistoryRemovalAuditSurvivesOrdinaryRebind(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "history-rebind-audit", 0, -1)
	deviceID := "extension:history-rebind-audit"
	now := time.Now().UTC().Truncate(time.Second)
	id := seedDeviceUserBinding(t, db, owner.ID, deviceID, "", now, "", nil)
	router := deviceUserRouter(owner)
	path := fmt.Sprintf("/api/user/devices/%d/remove", id)
	for i := 0; i < 2; i++ {
		result, _ := doDeviceUser(t, router, http.MethodPost, path, nil)
		if result.Code != http.StatusOK {
			t.Fatalf("remove: %d %s", result.Code, result.Body)
		}
	}
	if decision, err := service.RegisterDevice(db, owner.ID, deviceID); err != nil || !decision.Allowed {
		t.Fatalf("remove prevented ordinary rebind: %+v %v", decision, err)
	}
	var logs []entity.Log
	if err := db.Where("module = ? AND user_id = ?", "device", owner.ID).Find(&logs).Error; err != nil || len(logs) != 1 {
		t.Fatalf("first removal must survive rebind exactly once: %d %v", len(logs), err)
	}
	if !strings.Contains(logs[0].NewAnswer, "user_removed") || !strings.Contains(logs[0].NewAnswer, fmt.Sprint(id)) {
		t.Fatalf("audit lost removal reason/binding identity: %s", logs[0].NewAnswer)
	}
}

func TestDeviceHistoryLegacyAccountBlockCannotSilentlyResumeOnUnblock(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "history-legacy-account-block", 0, -1)
	deviceID := "extension:history-legacy-block"
	created := time.Now().UTC().Truncate(time.Second)
	id := seedDeviceUserBinding(t, db, owner.ID, deviceID, "", created.Add(-time.Hour), "", nil)
	rule := entity.UserDeviceBlock{UserID: owner.ID, DeviceID: deviceID, Reason: "legacy reason", CreatedAt: created}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	usedWhileBlocked, err := service.CountDevices(db, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	router := deviceUserRouter(owner)
	res, _ := doDeviceUser(t, router, http.MethodDelete, fmt.Sprintf("/api/user/devices/blocks/%d", rule.ID), nil)
	if res.Code != http.StatusOK {
		t.Fatalf("legacy explicit unblock failed: %d %s", res.Code, res.Body)
	}
	var row entity.AccountDeviceBinding
	if err := db.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	usedAfterUnblock, err := service.CountDevices(db, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usedWhileBlocked != 0 || usedAfterUnblock != 0 || row.RevokedAt == nil || !row.RevokedAt.Equal(created) {
		t.Fatalf("legacy block/unblock silently resumes a binding: blocked slots=%d after unblock=%d revoke=%v", usedWhileBlocked, usedAfterUnblock, row.RevokedAt)
	}
}
