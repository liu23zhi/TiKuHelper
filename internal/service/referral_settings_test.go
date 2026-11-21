package service

import (
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
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

func TestAdminReferralInvitesBackfillCurrentPage(t *testing.T) {
	db := newReferralTestDB(t)
	owner := seedReferralUser(t, db, 961, "existing", 0)
	existing, err := RegisterReferralInviteForNewUser(db, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	older := seedReferralUser(t, db, 962, "untouched-page", 0)
	target := seedReferralUser(t, db, 963, "legacy-target", owner.ID)
	phoneAccount := seedReferralUser(t, db, 960, "phone-account", 0)
	phone := "13800138000"
	if err := db.Model(&phoneAccount).Update("phone", phone).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&target).Updates(map[string]any{"balance": 8.5, "gift_balance": 2.75}).Error; err != nil {
		t.Fatal(err)
	}

	first, err := ListReferralInvitesWithDB(db, "", 1, 1)
	if err != nil || first.Total != 4 || len(first.Data) != 1 || first.Data[0].UserID != target.ID || first.Data[0].Code == "" || first.Data[0].CreatedAt == nil {
		t.Fatalf("first page must provision its existing account: %+v, %v", first, err)
	}
	var missing int64
	if err := db.Model(&entity.ReferralInvite{}).Where("user_id = ?", older.ID).Count(&missing).Error; err != nil || missing != 0 {
		t.Fatalf("listing one page provisioned a different page: %d, %v", missing, err)
	}
	phoneResult, err := ListReferralInvitesWithDB(db, phone, 1, 20)
	if err != nil || phoneResult.Total != 1 || len(phoneResult.Data) != 1 || phoneResult.Data[0].UserID != phoneAccount.ID || phoneResult.Data[0].Code == "" {
		t.Fatalf("phone search must provision its own result: %+v, %v", phoneResult, err)
	}
	for _, keyword := range []string{target.Username, first.Data[0].Code} {
		list, err := ListReferralInvitesWithDB(db, keyword, 1, 20)
		if err != nil || list.Total != 1 || len(list.Data) != 1 || list.Data[0].Code != first.Data[0].Code {
			t.Fatalf("search %q must find the same invite: %+v, %v", keyword, list, err)
		}
	}

	type outcome struct {
		list ReferralInviteAdminResult
		err  error
	}
	var workers sync.WaitGroup
	results := make(chan outcome, 4)
	for i := 0; i < cap(results); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			list, err := ListReferralInvitesWithDB(db, older.Username, 1, 20)
			results <- outcome{list, err}
		}()
	}
	workers.Wait()
	close(results)
	var code string
	for result := range results {
		if result.err != nil || len(result.list.Data) != 1 || result.list.Data[0].Code == "" {
			t.Fatalf("concurrent list failed: %+v, %v", result.list, result.err)
		}
		if code == "" {
			code = result.list.Data[0].Code
		}
		if result.list.Data[0].Code != code {
			t.Fatalf("concurrent list returned different codes")
		}
	}

	list, err := ListReferralInvitesWithDB(db, "", 1, 20)
	if err != nil || len(list.Data) != 4 || list.Data[2].Code != existing.Code || list.Data[2].InvitedCount != 1 {
		t.Fatalf("existing code or count changed: %+v, %v", list, err)
	}
	codes := make(map[string]bool, len(list.Data))
	for _, item := range list.Data {
		if item.Code == "" || codes[item.Code] {
			t.Fatalf("missing or duplicate code: %+v", item)
		}
		codes[item.Code] = true
	}
	var stored entity.User
	if err := db.First(&stored, target.ID).Error; err != nil || stored.Balance != 8.5 || stored.GiftBalance != 2.75 || stored.InvitedByUserID != owner.ID || stored.InvitedAt != nil {
		t.Fatalf("invite backfill altered wallet or attribution: %+v, %v", stored, err)
	}
}
