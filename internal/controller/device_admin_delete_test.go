package controller

import (
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
	if response := deviceAdminRequest(router, http.MethodDelete, path, ""); response.Code != http.StatusConflict {
		t.Fatalf("active binding deleted: %d %s", response.Code, response.Body)
	}
	if _, err := service.BanDevice(db, service.DeviceBanRequest{DeviceID: row.DeviceID, Reason: "test", CreatedBy: actor.ID}); err != nil {
		t.Fatal(err)
	}
	if response := deviceAdminRequest(router, http.MethodDelete, path, ""); response.Code != http.StatusOK {
		t.Fatalf("revoked deletion failed: %d %s", response.Code, response.Body)
	}
	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("id = ?", row.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("binding retained: %d %v", count, err)
	}
	bans, err := service.LoadActiveDeviceBansWithDB(db, []string{row.DeviceID}, time.Now())
	if err != nil || len(bans) != 1 {
		t.Fatalf("deletion bypassed device ban: %+v %v", bans, err)
	}
	if response := deviceAdminRequest(deviceAdminTestRouter(nil), http.MethodDelete, path, ""); response.Code == http.StatusOK {
		t.Fatal("unauthenticated deletion accepted")
	}
}
