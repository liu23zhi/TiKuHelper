package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
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

func TestDeviceHistoryAdminUnbindWritesOneAttributedAuditBeforeRebind(t *testing.T) {
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	admin := seedDeviceAdminActor(t, db, 740, "fixture-history-admin")
	row := seedDeviceAdminBinding(t, db, 741, "extension:review-audit-fixture")
	router := deviceAdminTestRouter(&admin)
	path := fmt.Sprintf("/api/admin/devices/%d/unbind", row.ID)
	for range 2 {
		res := deviceAdminRequest(router, http.MethodPost, path, "")
		if res.Code != http.StatusOK {
			t.Fatalf("admin unbind: %d %s", res.Code, res.Body)
		}
	}
	if decision, err := service.RegisterDevice(db, row.UserID, row.DeviceID); err != nil || !decision.Allowed {
		t.Fatalf("normal allowed rebind: %+v %v", decision, err)
	}
	var logs []entity.Log
	if err := db.Where("module = ?", "device").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		for _, log := range logs {
			t.Logf("actor=%d outcome=%s", log.UserID, log.NewAnswer)
		}
		t.Fatalf("only one successful actual unbind is audited; got %d", len(logs))
	}
	var outcome struct {
		TargetUserID int32
		RequestID    string
		Remark       string
		RevokedAt    time.Time
	}
	if err := json.Unmarshal([]byte(logs[0].NewAnswer), &outcome); err != nil {
		t.Fatal(err)
	}
	if logs[0].UserID != admin.ID || logs[0].Username != admin.Username ||
		outcome.TargetUserID != row.UserID || outcome.RequestID != fmt.Sprint(row.ID) || outcome.Remark != "admin_unbound" ||
		outcome.RevokedAt.IsZero() || strings.Contains(logs[0].NewAnswer, row.DeviceID) {
		t.Fatalf("audit missing actor, target binding, reason or time, or leaking raw identifier: owner=%d outcome=%s",
			logs[0].UserID, logs[0].NewAnswer)
	}
}

func TestDeviceHistoryAdminUnbindRollsBackWhenAuditCannotCommit(t *testing.T) {
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	admin := seedDeviceAdminActor(t, db, 742, "fixture-audit-failure-admin")
	row := seedDeviceAdminBinding(t, db, 743, "extension:fixture-admin-audit-failure")
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-device-audit", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameLog {
			tx.AddError(fmt.Errorf("synthetic device audit write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	res := deviceAdminRequest(deviceAdminTestRouter(&admin), http.MethodPost, fmt.Sprintf("/api/admin/devices/%d/unbind", row.ID), "")
	var latest entity.AccountDeviceBinding
	if err := db.First(&latest, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&entity.Log{}).Where("module = ?", "device").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusInternalServerError || latest.RevokedAt != nil || count != 0 {
		t.Fatalf("unaudited unbind left state: status=%d revoked=%v successful audits=%d", res.Code, latest.RevokedAt, count)
	}
}

func TestDeviceHistoryRebindPreservesLegacyRevocationWithoutInventingActor(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "fixture-legacy-evidence", 0, -1)
	reason := "idle_timeout"
	stamp := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	deviceID := "extension:fixture-legacy-audit"
	id := seedDeviceUserBinding(t, db, owner.ID, deviceID, "", stamp.Add(-time.Hour), reason, &stamp)
	// Older opaque entries must not be rewritten or used to manufacture an actor.
	if err := db.Create(&entity.Log{UserID: 333, Module: "device", Description: "legacy opaque", NewAnswer: "{}", CreateTime: stamp}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		decision, err := service.RegisterDevice(db, owner.ID, deviceID)
		if err != nil || !decision.Allowed {
			t.Fatalf("rebind: %+v %v", decision, err)
		}
	}
	var logs []entity.Log
	if err := db.Where("module = ?", "device").Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].NewAnswer != "{}" || logs[1].UserID != 0 || logs[1].Username != "" ||
		!strings.Contains(logs[1].NewAnswer, fmt.Sprintf("\"requestId\":\"%d\"", id)) ||
		!strings.Contains(logs[1].NewAnswer, stamp.Format(time.RFC3339)) || !strings.Contains(logs[1].NewAnswer, reason) {
		t.Fatalf("legacy evidence omitted, rewritten, or actor invented: %+v", logs)
	}
}

func TestDeviceHistoryUserListUsesOneSnapshotAcrossConcurrentBans(t *testing.T) {
	for _, scope := range []string{"account", "global"} {
		t.Run(scope, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.db")
			open := func() *gorm.DB {
				db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { sqlDB.Close() })
				return db
			}
			reader, writer := open(), open()
			if err := writer.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
				t.Fatal(err)
			}
			if err := writer.AutoMigrate(&entity.AccountDeviceBinding{}, &entity.UserDeviceBlock{}, &entity.DeviceLifecycleLock{}, &entity.BlacklistEntry{}, &entity.Log{}); err != nil {
				t.Fatal(err)
			}
			previous := deviceUserDBOverride
			deviceUserDBOverride = reader
			t.Cleanup(func() { deviceUserDBOverride = previous })
			owner := &entity.User{ID: 911, Username: "snapshot-owner"}
			deviceID := "extension:fixture-snapshot"
			id := seedDeviceUserBinding(t, writer, owner.ID, deviceID, "", time.Now(), "", nil)
			seedDeviceUserBinding(t, writer, 912, "extension:other-account", "", time.Now(), "", nil)
			interleaved := false
			cut := entity.TableNameUserDeviceBlock
			if scope == "global" {
				cut = entity.TableNameAccountDeviceBinding
			}
			if err := reader.Callback().Query().After("gorm:query").Register("test:commit-between-history-reads", func(tx *gorm.DB) {
				if interleaved || tx.Statement.Table != cut {
					return
				}
				interleaved = true
				var err error
				if scope == "global" {
					_, err = service.BanDevice(writer, service.DeviceBanRequest{DeviceID: deviceID, Reason: "fixture"})
				} else {
					_, err = service.BlockAccountDevice(writer, id, service.DeviceHistoryActor{UserID: owner.ID}, "fixture")
				}
				if err != nil {
					t.Errorf("fixture writer failed: %v", err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			router := deviceUserRouter(owner)
			query := "/api/user/devices?userId=912"
			pre, snapshot := doDeviceUser(t, router, http.MethodGet, query, nil)
			items := deviceUserItems(t, snapshot)
			if pre.Code != http.StatusOK || !interleaved || len(items) != 1 || items[0]["id"] != float64(id) ||
				items[0]["status"] != "active" || items[0]["revokedAt"] != nil || items[0]["deletable"] != false ||
				snapshot.Data["active"] != float64(1) || snapshot.Data["removed"] != float64(0) || snapshot.Data["blocked"] != float64(0) {
				t.Fatalf("mixed states or owner scope leaked within the original snapshot: %d %s", pre.Code, pre.Body)
			}
			post, final := doDeviceUser(t, router, http.MethodGet, query, nil)
			items = deviceUserItems(t, final)
			if post.Code != http.StatusOK || len(items) != 1 || items[0]["status"] != "blocked" || items[0]["revokedAt"] == nil ||
				items[0]["deletable"] != false || final.Data["active"] != float64(0) || final.Data["blocked"] != float64(1) {
				t.Fatalf("next request did not see committed block: %d %s", post.Code, post.Body)
			}
		})
	}
}

func TestDeviceHistoryRebindRetainsOriginalWithdrawalWhenAuditCannotCommit(t *testing.T) {
	db := newDeviceUserTestDB(t, true)
	owner := seedDeviceUserUser(t, db, "fixture-legacy-write-fails", 0, -1)
	when := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	deviceID := "extension:fixture-legacy-audit-failure"
	id := seedDeviceUserBinding(t, db, owner.ID, deviceID, "", when, "idle_timeout", &when)
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-legacy-audit", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameLog {
			tx.AddError(fmt.Errorf("synthetic archive failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterDevice(db, owner.ID, deviceID); err == nil {
		t.Fatal("legacy details must not be erased when their audit fails")
	}
	var row entity.AccountDeviceBinding
	if err := db.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	if row.RevokedAt == nil || !row.RevokedAt.Equal(when) || row.RevokedReason != "idle_timeout" || !row.LastSeenAt.Equal(when) {
		t.Fatalf("failed rebind altered legacy facts: %+v", row)
	}
}
