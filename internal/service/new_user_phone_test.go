package service

import (
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestCreateUserWithBonusChecksPhoneAliasesInFinalTransaction(t *testing.T) {
	db := newPhoneBindingTestDB(t)
	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	legacyPhone := "+8613800000000"
	if err := db.Create(&entity.User{Username: "legacy-bonus-phone-owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &legacyPhone}).Error; err != nil {
		t.Fatal(err)
	}
	canonical := "13800000000"
	user := entity.User{Username: "new_bonus_phone_owner", Password: "hashed", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &canonical}
	err := CreateUserWithNewUserBonusWithDB(db, &user, NewUserBonusOptions{Enabled: true, Amount: 1, Source: RegistrationBonusSourcePublicRegister})
	if err == nil || !strings.Contains(err.Error(), "手机号") {
		t.Fatalf("expected final transaction alias conflict, got %v", err)
	}
	var users, bills int64
	db.Model(&entity.User{}).Where("username = ?", user.Username).Count(&users)
	db.Model(&entity.BillingRecord{}).Count(&bills)
	if users != 0 || bills != 0 {
		t.Fatalf("failed registration must not create user or bonus: users=%d bills=%d", users, bills)
	}
}
