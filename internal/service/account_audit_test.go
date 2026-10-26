package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestAccountAuditStoresOnlySafeFieldsAndUsesTransaction(t *testing.T) {
	db := newLocationTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	err := WriteAccountAuditWithDB(tx, AccountAuditEvent{
		UserID: 3, Username: "anonymous", Module: "access_token", Action: 0,
		Description: strings.Repeat("账户", 150), CorrelationID: "token:8",
		Request: map[string]any{"name": "测试令牌", "tokenValue": "secret-token", "password": "secret-password"},
		Result:  map[string]any{"status": "success", "tokenId": 8, "after": map[string]any{"name": "已修改", "smsCode": "123456"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var entry entity.Log
	if err := tx.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.UserID != 3 || entry.Username != "anonymous" || len([]rune(entry.Description)) > 240 {
		t.Fatalf("invalid audit identity or description: %+v", entry)
	}
	if strings.Contains(entry.OldAnswer+entry.NewAnswer, "secret-") || strings.Contains(entry.NewAnswer, "123456") {
		t.Fatalf("audit leaked a secret: %s %s", entry.OldAnswer, entry.NewAnswer)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(entry.NewAnswer), &result); err != nil || result["status"] != "success" {
		t.Fatalf("invalid safe snapshot: %s err=%v", entry.NewAnswer, err)
	}
	tx.Rollback()
	var count int64
	if err := db.Model(&entity.Log{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("audit must participate in caller transaction: count=%d err=%v", count, err)
	}
}

func TestBindAccountVerificationLogsUsesExactConsumedEvidence(t *testing.T) {
	db := newLocationTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}, &entity.CaptchaToken{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.Create(&entity.CaptchaToken{Token: "final-token", ChallengeID: "remote:final", Scene: CaptchaSceneLoginSMS, Status: CaptchaTokenStatusConsumed, ConsumedAt: &now, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	entries := []entity.Log{
		{Module: "captcha", CorrelationID: "remote:final"},
		{Module: "captcha", CorrelationID: "local:before-sms"},
		{Module: "sms", CorrelationID: "sms:8"},
		{Module: "captcha", CorrelationID: "remote:unused"},
		{Module: "captcha", CorrelationID: "remote:final", UserID: 9, Username: "owner"},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	user := entity.User{ID: 3, Username: "anonymous"}
	evidence := SMSVerificationEvidence{SMSRecordID: 8, CaptchaCorrelationID: "local:before-sms"}
	for i := 0; i < 2; i++ {
		if err := BindAccountVerificationLogsWithDB(db.Model(&entity.User{}), user, "final-token", CaptchaSceneLoginSMS, evidence); err != nil {
			t.Fatal(err)
		}
	}
	var rows []entity.Log
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if rows[i].UserID != user.ID || rows[i].Username != user.Username {
			t.Fatalf("consumed verification was not bound: %+v", rows[i])
		}
	}
	if rows[3].UserID != 0 || rows[4].UserID != 9 {
		t.Fatalf("unrelated or previously owned verification was changed: %+v", rows)
	}
	if err := BindAccountVerificationLogsWithDB(db, user, "final-token", CaptchaSceneRegisterSubmit, SMSVerificationEvidence{}); err == nil {
		t.Fatal("a different scene must not bind the consumed token")
	}
}
