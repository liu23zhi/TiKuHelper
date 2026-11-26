package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
	"time"
)

func TestPortraitInventoryUsesTheSameScopeForLiveAndArchivedUsers(t *testing.T) {
	db := newPortraitTestDB(t)
	if err := db.AutoMigrate(&entity.AccountDeviceBinding{}, &entity.RechargeOrder{}); err != nil {
		t.Fatal(err)
	}
	user := entity.User{Username: "portrait-archive-target", Role: "user", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	for _, device := range []entity.AccountDeviceBinding{{UserID: user.ID, DeviceID: "target"}, {UserID: user.ID + 1, DeviceID: "other"}} {
		if err := db.Create(&device).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&entity.RechargeOrder{UserID: user.ID, OrderNo: "portrait-order", Status: "paid", Amount: 10}).Error; err != nil {
		t.Fatal(err)
	}
	build := func(id int32) UserPortrait {
		p, e := BuildUserPortraitWithDB(db, PortraitScope{UserID: id})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	get := func(p UserPortrait, key string) int64 {
		for _, item := range p.Insights {
			if item.Key == key {
				return item.Total
			}
		}
		return -1
	}
	if get(build(0), "devices") != 2 || get(build(user.ID), "devices") != 1 || get(build(user.ID), "recharges") != 1 {
		t.Fatal("portrait counts leaked another user's devices")
	}
	now := time.Now()
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	archived := build(user.ID)
	if archived.Base == nil || archived.Base.UserID != user.ID || get(archived, "devices") != 1 {
		t.Fatal("archived profile lost its history")
	}
}

func TestDeviceHistoryPortraitMatchesVisibleAdminRows(t *testing.T) {
	db := newDevicePoolTestDB(t)
	const uid int32 = 9863
	actor := DeviceHistoryActor{UserID: uid}
	hidden := seedDeviceBinding(t, db, uid, "extension:fixture-hide", true)
	seedDeviceBinding(t, db, uid, "extension:fixture-active", false)
	seedDeviceBinding(t, db, uid, "extension:fixture-removed", true)
	personal := seedDeviceBinding(t, db, uid, "extension:fixture-personal-ban", false)
	global := seedDeviceBinding(t, db, uid, "extension:fixture-global-ban", false)
	seedDeviceBinding(t, db, uid+1, "extension:fixture-unrelated", false)
	if _, err := ClearDeviceHistory(db, hidden.ID, uid, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := BlockAccountDevice(db, personal.ID, actor, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: global.DeviceID, Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	list, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{UserID: uid}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	var portrait UserPortrait
	if err := fillPortraitInventory(db, PortraitScope{UserID: uid}, &portrait); err != nil {
		t.Fatal(err)
	}
	for _, insight := range portrait.Insights {
		if insight.Key != "devices" {
			continue
		}
		var counts = map[string]int64{}
		for _, item := range insight.Items {
			counts[item.Label] = item.Count
		}
		if list.Total != 4 || list.Active != 1 || list.Removed != 1 || list.Blocked != 2 ||
			insight.Total != list.Total || counts["使用中"] != list.Active ||
			counts["已移除"] != list.Removed || counts["已拉黑"] != list.Blocked {
			t.Fatalf("device page / portrait disagree: list=%d/%d/%d/%d portrait=%d/%+v", list.Total, list.Active, list.Removed, list.Blocked, insight.Total, counts)
		}
		var retained entity.AccountDeviceBinding
		if err := db.First(&retained, hidden.ID).Error; err != nil || retained.HistoryHiddenAt == nil || retained.RevokedAt == nil {
			t.Fatalf("underlying evidence was lost: %+v %v", retained, err)
		}
		return
	}
	t.Fatal("device insights missing")
}
