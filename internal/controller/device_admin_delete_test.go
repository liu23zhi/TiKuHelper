package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestAdminDeviceDeleteRequiresRevokedBindingAndPreservesBan(t *testing.T) {
	db := newDeviceAdminControllerTestDB(t, true)
	installDeviceAdminTestDB(t, db)
	actor := seedDeviceAdminActor(t, db, 611, "delete-admin")
	row := seedDeviceAdminBinding(t, db, actor.ID, "exe:delete-banned")
	router := deviceAdminTestRouter(&actor)
	path := fmt.Sprintf("/api/admin/devices/%d", row.ID)
	checkConflict := func(path string) {
		t.Helper()
		response := deviceAdminRequest(router, http.MethodDelete, path, "")
		var body map[string]string
		if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
			len(body) != 1 || body["message"] != "只能删除已移除且未拉黑的设备历史" {
			t.Errorf("expected one complete 409 JSON response: %d %s", response.Code, response.Body)
		}
	}
	checkConflict(path)
	if _, err := service.BanDevice(db, service.DeviceBanRequest{DeviceID: row.DeviceID, Reason: "test", CreatedBy: actor.ID}); err != nil {
		t.Fatal(err)
	}
	checkConflict(path)
	if err := db.AutoMigrate(&entity.UserDeviceBlock{}); err != nil {
		t.Fatal(err)
	}
	personal := seedDeviceAdminBinding(t, db, actor.ID, "exe:delete-account-blocked")
	if _, err := service.BlockAccountDevice(db, personal.ID, service.DeviceHistoryActor{UserID: actor.ID}, "fixture"); err != nil {
		t.Fatal(err)
	}
	checkConflict(fmt.Sprintf("/api/admin/devices/%d", personal.ID))
	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("id = ?", row.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("blacklisted binding lost: %d %v", count, err)
	}
	bans, err := service.LoadActiveDeviceBansWithDB(db, []string{row.DeviceID}, time.Now())
	if err != nil || len(bans) != 1 {
		t.Fatalf("deletion bypassed device ban: %+v %v", bans, err)
	}
	if response := deviceAdminRequest(deviceAdminTestRouter(nil), http.MethodDelete, path, ""); response.Code == http.StatusOK {
		t.Fatal("unauthenticated deletion accepted")
	}
}
