package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newPhoneBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:phone-binding-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func strPtr(value string) *string {
	v := value
	return &v
}

func TestNormalizeMainlandPhoneRejectsInvalidValues(t *testing.T) {
	phone, err := NormalizeMainlandPhone(" 138-0000-0000 ")
	if err != nil {
		t.Fatalf("expected normalized phone, got %v", err)
	}
	if phone != "13800000000" {
		t.Fatalf("expected normalized phone 13800000000, got %s", phone)
	}

	if _, err := NormalizeMainlandPhone("12345"); err == nil {
		t.Fatal("expected invalid phone to fail")
	}
}

func TestNormalizePhoneAcceptsMainlandAndLegacy86NumbersOnly(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "13800000000", want: "13800000000"},
		{name: "legacy prefix", input: "+86 138-0000-0000", want: "13800000000"},
		{name: "whitespace", input: " +86\t138 0000 0000 ", want: "13800000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phone, region, err := NormalizePhone(tc.input)
			if err != nil {
				t.Fatalf("normalize mainland phone: %v", err)
			}
			if phone != tc.want || region != PhoneRegionMainland {
				t.Fatalf("unexpected mainland phone=%s region=%s", phone, region)
			}
		})
	}

	for _, input := range []string{"+1 415-555-2671", "+852 13800000000", "1380000000a", "138000000000"} {
		if _, _, err := NormalizePhone(input); err == nil {
			t.Fatalf("expected non-mainland phone %q to fail", input)
		}
	}
}

func TestMaskPhoneNormalizesLegacy86Numbers(t *testing.T) {
	if masked := MaskPhone("13800000000"); masked != "138****0000" {
		t.Fatalf("unexpected mainland masked phone %s", masked)
	}
	if masked := MaskPhone("+86 13800000000"); masked != "138****0000" {
		t.Fatalf("unexpected legacy mainland masked phone %s", masked)
	}
	if masked := MaskPhone("+14155552671"); masked != "+141****2671" {
		t.Fatalf("unexpected legacy international masked phone %s", masked)
	}
}

func TestPhoneLookupCandidatesAndHistoricalConflict(t *testing.T) {
	canonical, err := NormalizeMainlandPhoneInput("+86 138-0000-0000")
	if err != nil {
		t.Fatalf("normalize phone input: %v", err)
	}
	aliases := PhoneLookupCandidates(canonical)
	if fmt.Sprint(aliases) != "[13800000000 +8613800000000]" {
		t.Fatalf("unexpected phone aliases %v", aliases)
	}

	db := newPhoneBindingTestDB(t)
	for id, stored := range map[int32]string{1: "13800000000", 2: "+8613800000000"} {
		if err := db.Create(&entity.User{ID: id, Username: fmt.Sprintf("user-%d", id), Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr(stored)}).Error; err != nil {
			t.Fatalf("create user %d: %v", id, err)
		}
	}
	users, err := FindUsersByPhoneAliases(db, canonical)
	if err != nil {
		t.Fatalf("find historical phone aliases: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected both historical aliases, got %d", len(users))
	}
}

func TestUpdateUserPhoneWithDBRejectsHistoricalAliasConflict(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	owner := entity.User{ID: 1, Username: "owner", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr("+8613800000000")}
	target := entity.User{ID: 2, Username: "target", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := UpdateUserPhoneWithDB(db, target.ID, "13800000000", false); err == nil {
		t.Fatal("expected historical +86 alias to be treated as occupied")
	}
}

func TestUpdateUserPhoneWithDBPreservesUnchangedLegacyNumber(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	boundAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	user := entity.User{ID: 1, Username: "legacy-owner", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr("+8613800000000"), PhoneBoundAt: &boundAt}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create legacy user: %v", err)
	}
	updated, err := UpdateUserPhoneWithDB(db, user.ID, "13800000000", false)
	if err != nil {
		t.Fatalf("save unchanged legacy phone: %v", err)
	}
	if updated.Phone == nil || *updated.Phone != "+8613800000000" || updated.PhoneBoundAt == nil || !updated.PhoneBoundAt.Equal(boundAt) {
		t.Fatalf("unchanged legacy phone was rewritten: %+v", updated)
	}
}

func TestUpdateUserPhoneWithDBRejectsCrossAccountAliasesEvenWhenForcedOrUnchanged(t *testing.T) {
	for _, target := range []int32{1, 3} {
		t.Run(fmt.Sprintf("target-%d", target), func(t *testing.T) {
			db := newPhoneBindingTestDB(t)
			for id, stored := range map[int32]string{1: "13800000000", 2: "+8613800000000"} {
				if err := db.Create(&entity.User{ID: id, Username: fmt.Sprintf("alias-owner-%d", id), Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr(stored)}).Error; err != nil {
					t.Fatalf("create alias owner: %v", err)
				}
			}
			if target == 3 {
				if err := db.Create(&entity.User{ID: target, Username: "target", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}).Error; err != nil {
					t.Fatalf("create target: %v", err)
				}
			}
			if _, err := UpdateUserPhoneWithDB(db, target, "13800000000", true); err == nil {
				t.Fatal("expected cross-account historical alias conflict to block forced/unchanged update")
			}
			users, err := FindUsersByPhoneAliases(db, "13800000000")
			if err != nil || len(users) != 2 {
				t.Fatalf("conflicting aliases must remain unchanged, users=%d err=%v", len(users), err)
			}
		})
	}
}

func TestUpdateUserPhoneWithDBAssignsAndClearsPhone(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	user := entity.User{ID: 1, Username: "target", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	updated, err := UpdateUserPhoneWithDB(db, 1, " 138-0000-0000 ", false)
	if err != nil {
		t.Fatalf("assign phone: %v", err)
	}
	if updated.Phone == nil || *updated.Phone != "13800000000" {
		t.Fatalf("expected phone to be assigned, got %+v", updated.Phone)
	}
	if updated.PhoneBoundAt == nil {
		t.Fatal("expected phone bound time to be written")
	}

	cleared, err := UpdateUserPhoneWithDB(db, 1, "", false)
	if err != nil {
		t.Fatalf("clear phone: %v", err)
	}
	if cleared.Phone != nil {
		t.Fatalf("expected cleared phone nil, got %+v", cleared.Phone)
	}
	if cleared.PhoneBoundAt != nil {
		t.Fatalf("expected cleared bound time nil, got %+v", cleared.PhoneBoundAt)
	}
}

func TestUpdateUserPhoneWithDBRejectsOccupiedPhoneWithoutForceReassign(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	now := time.Now()
	owner := entity.User{ID: 1, Username: "owner", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr("13800000000"), PhoneBoundAt: &now}
	target := entity.User{ID: 2, Username: "target", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	if _, err := UpdateUserPhoneWithDB(db, 2, "13800000000", false); err == nil {
		t.Fatal("expected occupied phone without force reassign to fail")
	}
}

func TestUpdateUserPhoneWithDBReassignsPhoneWhenForced(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	now := time.Now()
	owner := entity.User{ID: 1, Username: "owner", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr("13800000000"), PhoneBoundAt: &now}
	target := entity.User{ID: 2, Username: "target", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}

	updated, err := UpdateUserPhoneWithDB(db, 2, "13800000000", true)
	if err != nil {
		t.Fatalf("force reassign phone: %v", err)
	}
	if updated.Phone == nil || *updated.Phone != "13800000000" {
		t.Fatalf("expected reassigned phone, got %+v", updated.Phone)
	}

	var reloadedOwner entity.User
	if err := db.Where("id = ?", 1).First(&reloadedOwner).Error; err != nil {
		t.Fatalf("reload owner: %v", err)
	}
	if reloadedOwner.Phone != nil || reloadedOwner.PhoneBoundAt != nil {
		t.Fatalf("expected old owner phone to be cleared, got %+v", reloadedOwner)
	}
}

func TestUpdateUserPhoneWithVerificationConsumesNewPhoneCode(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("migrate sms verification table: %v", err)
	}
	user := entity.User{ID: 8, Username: "phone-change", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&entity.SMSVerificationCode{
		Phone:     "13900000000",
		Scene:     SMSSceneChangePhone,
		CodeHash:  hashSMSCode("123456"),
		Status:    SMSCodeStatusPending,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed sms code: %v", err)
	}

	updated, err := UpdateUserPhoneWithVerificationWithDB(db, 8, "13900000000", "123456")
	if err != nil {
		t.Fatalf("update phone with verification: %v", err)
	}
	if updated.Phone == nil || *updated.Phone != "13900000000" {
		t.Fatalf("expected new phone, got %+v", updated.Phone)
	}
	if updated.PhoneBoundAt == nil {
		t.Fatal("expected phone bound time")
	}
	if err := VerifySMSCodeWithDB(db, "13900000000", SMSSceneChangePhone, "123456"); err == nil {
		t.Fatal("expected verification code to be consumed once")
	}
}

func TestUpdateUserPhoneWithVerificationRollsBackConsumedCodeOnBindingFailure(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("migrate sms verification table: %v", err)
	}
	user := entity.User{ID: 8, Username: "phone-change-failure", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	record := entity.SMSVerificationCode{Phone: "+8613900000000", Scene: SMSSceneChangePhone, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(5 * time.Minute)}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("seed legacy sms code: %v", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("phone-binding-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameUser {
			tx.AddError(fmt.Errorf("forced binding failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if _, err := UpdateUserPhoneWithVerificationWithDB(db, user.ID, "13900000000", "123456"); err == nil {
		t.Fatal("expected forced binding failure")
	}
	var reloaded entity.SMSVerificationCode
	if err := db.First(&reloaded, record.ID).Error; err != nil {
		t.Fatalf("reload SMS code: %v", err)
	}
	if reloaded.Status != SMSCodeStatusPending || reloaded.ConsumedAt != nil {
		t.Fatalf("binding failure must roll back code consumption, got %+v", reloaded)
	}
}
