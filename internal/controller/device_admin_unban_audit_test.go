package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

const ownedUnbanDevice = "exe:owned-unban-fixture-321"

func ownedUnbanDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newDeviceAdminControllerTestDB(t, true)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.UserDeviceBlock{}); err != nil {
		t.Fatal(err)
	}
	installDeviceAdminTestDB(t, db)
	setBlacklistAdminSwitch(t, db, true)
	return db
}

func ownedUnbanBan(t *testing.T, router *gin.Engine) service.DeviceBanResult {
	t.Helper()
	out := deviceAdminRequest(router, http.MethodPost, "/api/admin/device-bans",
		fmt.Sprintf("{\"deviceId\":%q,\"reason\":\"owned test rule\"}", ownedUnbanDevice))
	var result service.DeviceBanResult
	if out.Code != http.StatusCreated || json.Unmarshal(out.Body.Bytes(), &result) != nil || result.EntryID <= 0 {
		t.Fatalf("fixture ban must register a rule, HTTP %d", out.Code)
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchDevice(ownedUnbanDevice); !hit {
		t.Fatal("fixture must be blocked in the real cache")
	}
	return result
}

func ownedUnbanAuditRows(t *testing.T, db *gorm.DB) []entity.Log {
	t.Helper()
	var rows []entity.Log
	if err := db.Where("module = ? AND action = ? AND description = ?", "device", 4, "解封设备").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAdminDeviceUnbanAuditsOnlyCommittedRuleDeletion(t *testing.T) {
	db := ownedUnbanDB(t)
	admin := seedDeviceAdminActor(t, db, 1201, "fixture-admin-one")
	otherAdmin := seedDeviceAdminActor(t, db, 1202, "fixture-admin-two")
	owner := entity.User{ID: 1203, Role: "user", Status: "enabled"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	first := seedDeviceAdminBinding(t, db, owner.ID, ownedUnbanDevice)
	second := seedDeviceAdminBinding(t, db, 1204, ownedUnbanDevice)
	router := deviceAdminTestRouter(&admin)
	ban := ownedUnbanBan(t, router)
	private := entity.UserDeviceBlock{UserID: owner.ID, DeviceID: ownedUnbanDevice, Reason: "owned separate block"}
	if err := db.Create(&private).Error; err != nil {
		t.Fatal(err)
	}
	var before []entity.AccountDeviceBinding
	if err := db.Where("id IN ?", []int64{first.ID, second.ID}).Order("id").Find(&before).Error; err != nil || len(before) != 2 {
		t.Fatalf("read pre-unban bindings: %v", err)
	}
	path := "/api/admin/device-bans/" + ownedUnbanDevice
	if out := deviceAdminRequest(deviceAdminTestRouter(&owner), http.MethodDelete, path, ""); out.Code != http.StatusForbidden {
		t.Fatalf("ordinary user: got HTTP %d, want 403", out.Code)
	}
	type outcome struct {
		actor entity.User
		code  int
		body  string
	}
	begin := make(chan struct{})
	results := make(chan outcome, 2)
	for _, actor := range []entity.User{admin, otherAdmin} {
		engine := deviceAdminTestRouter(&actor)
		go func() {
			<-begin
			out := deviceAdminRequest(engine, http.MethodDelete, path, "")
			results <- outcome{actor, out.Code, out.Body.String()}
		}()
	}
	close(begin)
	var winner entity.User
	for range 2 {
		out := <-results
		var body map[string]any
		if out.code != http.StatusOK || json.Unmarshal([]byte(out.body), &body) != nil ||
			len(body) != 2 || body["deviceId"] != ownedUnbanDevice || (body["deleted"] != float64(0) && body["deleted"] != float64(1)) {
			t.Fatalf("unban must preserve deviceId/deleted contract, got HTTP %d", out.code)
		}
		if body["deleted"] == float64(1) {
			if winner.ID != 0 {
				t.Fatal("global rule was deleted twice")
			}
			winner = out.actor
		}
	}
	rows := ownedUnbanAuditRows(t, db)
	t.Logf("two concurrent actors, winner_found=%t transition_audits=%d", winner.ID != 0, len(rows))
	if winner.ID == 0 || len(rows) != 1 || rows[0].UserID != winner.ID || rows[0].Username != winner.Username {
		t.Fatal("one successful deletion must record exactly one matching administrator audit")
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(rows[0].NewAnswer), &details); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(details, map[string]any{
		"name": service.MaskBlacklistDeviceID(ownedUnbanDevice), "scope": "global", "requestId": strconv.FormatInt(ban.EntryID, 10),
	}) || rows[0].ClientIP != "" {
		t.Fatal("audit must link the prior rule using the masked device and safe fields only")
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchDevice(ownedUnbanDevice); hit {
		t.Fatal("cache still blocks after successful unban")
	}
	var after []entity.AccountDeviceBinding
	if err := db.Where("id IN ?", []int64{first.ID, second.ID}).Order("id").Find(&after).Error; err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("global unban must leave both owners' bindings byte-for-byte equivalent as rows")
	}
	if err := db.First(&entity.UserDeviceBlock{}, private.ID).Error; err != nil {
		t.Fatal("global unban must preserve the independent personal rule")
	}
	// A device with no global rule should not create a lifecycle record or audit.
	absentDevice := "exe:owned-unban-absent"
	out := deviceAdminRequest(router, http.MethodDelete, "/api/admin/device-bans/"+absentDevice, "")
	var count int64
	if err := db.Model(&entity.DeviceLifecycleLock{}).Where("device_key = ?", service.BlacklistPatternHashHex(absentDevice)).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusOK || count != 0 || len(ownedUnbanAuditRows(t, db)) != 1 {
		t.Fatal("missing-rule unban must be a no-op")
	}
}

func TestAdminDeviceUnbanRollsBackOnAuditWriteFailure(t *testing.T) {
	db := ownedUnbanDB(t)
	admin := seedDeviceAdminActor(t, db, 1205, "fixture-audit-failure-admin")
	router := deviceAdminTestRouter(&admin)
	ban := ownedUnbanBan(t, router)
	if err := db.Exec("CREATE TRIGGER unban_audit_fixture_failure BEFORE INSERT ON logs WHEN NEW.description='解封设备' BEGIN SELECT RAISE(ABORT,'owned audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	out := deviceAdminRequest(router, http.MethodDelete, "/api/admin/device-bans/"+ownedUnbanDevice, "")
	var rule entity.BlacklistEntry
	ruleErr := db.Take(&rule, ban.EntryID).Error
	_, hit := service.DefaultBlacklistCache().Snapshot().MatchDevice(ownedUnbanDevice)
	rows := ownedUnbanAuditRows(t, db)
	t.Logf("audit INSERT failed: HTTP %d rule_retained=%t cache_blocks=%t committed_audits=%d", out.Code, ruleErr == nil, hit, len(rows))
	if out.Code != http.StatusInternalServerError || ruleErr != nil || !hit || len(rows) != 0 {
		t.Fatal("failed audit must roll back the global delete and leave the cache active")
	}
}
