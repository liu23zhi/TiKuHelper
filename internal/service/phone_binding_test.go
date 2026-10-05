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

func TestNormalizePhoneAcceptsMainlandAndInternationalNumbers(t *testing.T) {
	mainland, region, err := NormalizePhone(" 138-0000-0000 ")
	if err != nil {
		t.Fatalf("normalize mainland phone: %v", err)
	}
	if mainland != "13800000000" || region != PhoneRegionMainland {
		t.Fatalf("unexpected mainland phone=%s region=%s", mainland, region)
	}

	international, region, err := NormalizePhone("+1 415-555-2671")
	if err != nil {
		t.Fatalf("normalize international phone: %v", err)
	}
	if international != "+14155552671" || region != PhoneRegionInternational {
		t.Fatalf("unexpected international phone=%s region=%s", international, region)
	}
}

func TestMaskPhoneSupportsInternationalNumbers(t *testing.T) {
	if masked := MaskPhone("13800000000"); masked != "138****0000" {
		t.Fatalf("unexpected mainland masked phone %s", masked)
	}
	if masked := MaskPhone("+14155552671"); masked != "+141****2671" {
		t.Fatalf("unexpected international masked phone %s", masked)
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
