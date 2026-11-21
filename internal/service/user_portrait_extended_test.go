package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestPortraitDailyWindowTimezoneScopeAndAccountDistributions(t *testing.T) {
	db := newPortraitTestDB(t)
	first := portraitSeedUser(t, db, 8501, "portrait-daily-one")
	second := portraitSeedUser(t, db, 8502, "portrait-daily-two")
	if err := db.Model(&entity.User{}).Where("id = ?", second.ID).Updates(map[string]any{"status": "disabled", "role": "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	archived := portraitSeedUser(t, db, 8503, "portrait-daily-archived")
	if err := db.Delete(&archived).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, record := range []entity.BillingRecord{
		{UserID: first.ID, Amount: 1.25, CreatedAt: time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)},
		{UserID: first.ID, Amount: 2.50, CreatedAt: time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)},
		{UserID: second.ID, Amount: 5, CreatedAt: time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)},
		{UserID: first.ID, Amount: 9, CreatedAt: now.AddDate(0, 0, -31)},
		{UserID: first.ID, Amount: 11, CreatedAt: now.AddDate(0, 0, 1)},
	} {
		portraitSeedBilling(t, db, record)
	}
	portrait := UserPortrait{GeneratedAt: now, Daily: []PortraitDailySlice{}, UserStatuses: []PortraitSourceSlice{}, UserRoles: []PortraitSourceSlice{}}
	if err := fillPortraitDailyAndAccounts(db, PortraitScope{}, &portrait); err != nil {
		t.Fatal(err)
	}
	if len(portrait.Daily) != 1 || portrait.Daily[0].Date != "2026-11-20" || portrait.Daily[0].Count != 3 || portrait.Daily[0].Amount != 8.75 {
		t.Fatalf("daily must use UTC+8, omit empty/old/future days: %+v", portrait.Daily)
	}
	if len(portrait.UserStatuses) != 2 || len(portrait.UserRoles) != 2 {
		t.Fatalf("real non-archived account groups: %+v %+v", portrait.UserStatuses, portrait.UserRoles)
	}
	for _, groups := range [][]PortraitSourceSlice{portrait.UserStatuses, portrait.UserRoles} {
		var count int64
		for _, group := range groups {
			count += group.Count
		}
		if count != 2 {
			t.Fatalf("archived user counted: %+v", groups)
		}
	}
	single := UserPortrait{GeneratedAt: now, Daily: []PortraitDailySlice{}}
	if err := fillPortraitDailyAndAccounts(db, PortraitScope{UserID: first.ID}, &single); err != nil {
		t.Fatal(err)
	}
	if len(single.Daily) != 1 || single.Daily[0].Count != 2 || single.Daily[0].Amount != 3.75 {
		t.Fatalf("single-user daily scope: %+v", single.Daily)
	}
}
