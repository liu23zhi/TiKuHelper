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
