package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
)

func TestReferralSettingsAndCodeGenerationWithoutConfiguration(t *testing.T) {
	db := newReferralTestDB(t)
	user := seedReferralUser(t, db, 951, "no-referral-config", 0)
	invite, err := RegisterReferralInviteForNewUser(db, user.ID)
	if err != nil || invite.Code == "" {
		t.Fatalf("registration must create code without rebate config: %+v %v", invite, err)
	}
	if GetReferralSettingsWithDB(db).Enabled || GetReferralSettingsWithDB(db).Rate != 0 {
		t.Fatal("missing reward config must never pay rewards")
	}
	order := seedReferralPaidOrder(t, db, "no-config-payment", user.ID, 100)
	if rebate, err := CreateReferralRebateInTx(db, order); err != nil || rebate != nil {
		t.Fatalf("missing config cannot issue a reward: %+v %v", rebate, err)
	}
	if err := SaveReferralSettingsWithDB(db, ReferralSettings{Enabled: true, Rate: 0.125}); err != nil {
		t.Fatal(err)
	}
	settings := GetReferralSettingsWithDB(db)
	if !settings.Enabled || settings.Rate != 0.125 {
		t.Fatalf("settings=%+v", settings)
	}
	if err := SaveReferralSettingsWithDB(db, ReferralSettings{Enabled: true, Rate: 1}); err == nil {
		t.Fatal("must reject invalid rate")
	}
	second := seedReferralUser(t, db, 952, "second-referral", user.ID)
	if _, err := RegisterReferralInviteForNewUser(db, second.ID); err != nil {
		t.Fatal(err)
	}
	list, err := ListReferralInvitesWithDB(db, "no-referral", 1, 20)
	if err != nil || list.Total != 1 || len(list.Data) != 1 || list.Data[0].InvitedCount != 1 || list.Data[0].CreatedAt == nil || list.Data[0].CreatedAt.IsZero() {
		t.Fatalf("admin inventory=%+v error=%v", list, err)
	}
	var count int64
	db.Model(&entity.ReferralRebate{}).Count(&count)
	if count != 0 {
		t.Fatal("code/config changes must not issue money")
	}
}
