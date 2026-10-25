package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestLocalCaptchaAuditUsesChallengeEvidenceWithoutSecrets(t *testing.T) {
	db := newCaptchaTestDB(t)
	previousGenerator := captchaCodeGenerator
	captchaCodeGenerator = func() string { return "AB12CD" }
	t.Cleanup(func() { captchaCodeGenerator = previousGenerator })
	challenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneLoginPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneLoginPassword, "WRONG", "127.0.0.1"); err == nil {
		t.Fatal("wrong answer must fail")
	}
	token, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneLoginPassword, "AB12CD", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var logs []entity.Log
	if err := db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected generate, failed check and successful check logs, got %+v", logs)
	}
	for _, entry := range logs {
		if entry.CorrelationID != "local:"+challenge.ChallengeID || entry.UserID != 0 {
			t.Fatalf("unexpected local captcha evidence: %+v", entry)
		}
		for _, secret := range []string{"AB12CD", "WRONG", token.Token, hashCaptchaValue("AB12CD"), "data:image/png"} {
			if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, secret) {
				t.Fatalf("captcha secret %q leaked into audit", secret)
			}
		}
	}
	if !strings.Contains(logs[1].Description, "result=failed") || !strings.Contains(logs[2].Description, "result=success") {
		t.Fatalf("unexpected verification statuses: %+v", logs)
	}
	if _, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneLoginPassword, "AB12CD", "127.0.0.1"); err == nil {
		t.Fatal("verified challenge must not generate another token")
	}
}

func TestConsumedCaptchaCorrelationRequiresActualConsumptionAndScene(t *testing.T) {
	db := newCaptchaTestDB(t)
	for _, challengeID := range []string{"local-id", "remote:remote-id"} {
		token := entity.CaptchaToken{Token: "token-" + challengeID, Scene: CaptchaSceneRegisterSubmit, ChallengeID: challengeID, Status: CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}
		if err := db.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := ConsumedCaptchaCorrelationIDWithDB(db, token.Token, token.Scene); err == nil {
			t.Fatal("active token must not provide consumption evidence")
		}
		if err := ConsumeCaptchaTokenWithDB(db, token.Token, token.Scene, "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
		expected := challengeID
		if !strings.HasPrefix(expected, "remote:") {
			expected = "local:" + expected
		}
		got, err := ConsumedCaptchaCorrelationIDWithDB(db, token.Token, token.Scene)
		if err != nil || got != expected {
			t.Fatalf("expected %q, got %q, err=%v", expected, got, err)
		}
		if _, err := ConsumedCaptchaCorrelationIDWithDB(db, token.Token, CaptchaSceneLoginSMS); err == nil {
			t.Fatal("different scene must not provide consumption evidence")
		}
	}
}

func TestSMSDetailedEvidencePreservesOnlyActuallyConsumedRecord(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	seedSMSSettings(t, db)
	previousSender, previousGenerator := smsSender, generateSMSCode
	calls := 0
	smsSender = func(SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(string, string) error { calls++; return nil })
	}
	generateSMSCode = func() string { return "123456" }
	t.Cleanup(func() { smsSender, generateSMSCode = previousSender, previousGenerator })
	evidence, err := SendSMSCodeDetailedWithDB(db, SendSMSCodeInput{Phone: "13800000000", Scene: SMSSceneRegister, RequestIP: "127.0.0.1", CaptchaCorrelationID: "remote:captcha-send"})
	if err != nil || evidence.SMSRecordID <= 0 || evidence.CaptchaCorrelationID != "remote:captcha-send" {
		t.Fatalf("unexpected send evidence: %+v, err=%v", evidence, err)
	}
	if calls != 1 {
		t.Fatalf("expected one actual provider call, got %d", calls)
	}
	var log entity.Log
	if err := db.Where("module = ?", "sms").First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if log.CorrelationID != fmt.Sprintf("sms:%d", evidence.SMSRecordID) || !strings.Contains(log.NewAnswer, "success") {
		t.Fatalf("SMS success log must use persisted record evidence: %+v", log)
	}
	if strings.Contains(log.OldAnswer+log.NewAnswer, "123456") || strings.Contains(log.OldAnswer, "13800000000") {
		t.Fatalf("SMS audit contains secrets: %+v", log)
	}
	if _, err := VerifySMSCodeDetailedWithDB(db, "13800000000", SMSSceneRegister, "000000"); err == nil {
		t.Fatal("wrong SMS code must not produce evidence")
	}
	used, err := VerifySMSCodeDetailedWithDB(db, "13800000000", SMSSceneRegister, "123456")
	if err != nil || used != evidence {
		t.Fatalf("consumption must return actual stored evidence: sent=%+v used=%+v err=%v", evidence, used, err)
	}
	if err := VerifySMSCodeWithDB(db, "13800000000", SMSSceneRegister, "123456"); err == nil {
		t.Fatal("old wrapper must still reject code reuse")
	}
}

func TestSMSDetailedVerificationEvidenceRollsBackWithCallerTransaction(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	record := entity.SMSVerificationCode{Phone: "13800000000", Scene: SMSSceneRegister, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute), CaptchaChallengeID: "local:captcha-used"}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("registration failed")
	err := db.Transaction(func(tx *gorm.DB) error {
		evidence, err := VerifySMSCodeDetailedInTx(tx, record.Phone, record.Scene, "123456")
		if err != nil || evidence.SMSRecordID != record.ID || evidence.CaptchaCorrelationID != record.CaptchaChallengeID {
			t.Fatalf("unexpected record evidence: %+v, err=%v", evidence, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var after entity.SMSVerificationCode
	if err := db.First(&after, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != SMSCodeStatusPending || after.ConsumedAt != nil {
		t.Fatalf("failed caller transaction consumed the SMS code: %+v", after)
	}
}

func TestRemoteCaptchaAuditUsesRequestedIDAndOmitsVerificationSecrets(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"success":true,"message":"ok","data":{"id":"different-response-id","captchaCode":"solution","token":"secret-token","image":"secret-image"}}`))
	}))
	t.Cleanup(server.Close)
	_, _, err := CheckRemoteCaptchaWithDB(RemoteCaptchaProxyInput{
		DB: db, APIBaseURL: server.URL, Client: server.Client(),
		Body: []byte(`{"id":"requested-id","code":"123456","captchaCode":"solution","data":{"answer":"private-answer","trackList":[{"x":100}]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var entry entity.Log
	if err := db.First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.CorrelationID != "remote:requested-id" {
		t.Fatalf("remote check must correlate actual request evidence: %+v", entry)
	}
	for _, secret := range []string{"123456", "solution", "secret-token", "secret-image", "private-answer"} {
		if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, secret) {
			t.Fatalf("verification secret %q leaked into audit: %+v", secret, entry)
		}
	}
}

func TestSMSAuditDistinguishesRejectedAndUnknownSendResults(t *testing.T) {
	for _, test := range []struct {
		name, message, expected string
	}{
		{"rejected", "短信宝余额不足", "failed"},
		{"unknown", "request failed with secret credential", "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newSMSVerificationTestDB(t)
			if err := db.AutoMigrate(&entity.Log{}); err != nil {
				t.Fatal(err)
			}
			seedSMSSettings(t, db)
			previous := smsSender
			calls := 0
			smsSender = func(SystemSettingDetail) SMSProvider {
				return stubSMSProvider(func(string, string) error { calls++; return errors.New(test.message) })
			}
			t.Cleanup(func() { smsSender = previous })
			if err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: "13800000000", Scene: SMSSceneRegister}); err == nil {
				t.Fatal("provider failure must be returned")
			}
			var entry entity.Log
			if err := db.Where("module = ?", "sms").First(&entry).Error; err != nil {
				t.Fatal(err)
			}
			if calls != 1 || entry.CorrelationID != "" || !strings.Contains(entry.NewAnswer, `"status":"`+test.expected+`"`) {
				t.Fatalf("unexpected actual-send result: calls=%d entry=%+v", calls, entry)
			}
			if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, "secret credential") {
				t.Fatalf("unknown upstream error must not leak: %+v", entry)
			}
			var records int64
			if err := db.Model(&entity.SMSVerificationCode{}).Count(&records).Error; err != nil || records != 0 {
				t.Fatalf("unconfirmed send must not create a usable verification code: count=%d err=%v", records, err)
			}
		})
	}
}

func TestSMSDetailedVerificationAcceptsLegacyRecordWithoutCaptchaEvidence(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	record := entity.SMSVerificationCode{Phone: "13800000000", Scene: SMSSceneLogin, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	evidence, err := VerifySMSCodeDetailedWithDB(db, record.Phone, record.Scene, "123456")
	if err != nil || evidence.SMSRecordID != record.ID || evidence.CaptchaCorrelationID != "" {
		t.Fatalf("legacy SMS authentication must remain available without inferred evidence: %+v err=%v", evidence, err)
	}
}

func TestCaptchaTokenConsumptionRejectsStateChangedAfterRead(t *testing.T) {
	db := newCaptchaTestDB(t)
	record := entity.CaptchaToken{Token: "private-token", ChallengeID: "challenge", Scene: CaptchaSceneLoginPassword, Status: CaptchaTokenStatusActive, ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	raced := false
	if err := db.Callback().Query().After("gorm:after_query").Register("simulate_competing_token_consumer", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameCaptchaToken && !raced {
			raced = true
			if err := db.Exec("UPDATE captcha_tokens SET status = ?, consumed_at = ? WHERE id = ?", CaptchaTokenStatusConsumed, time.Now(), record.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeCaptchaTokenInTx(db, record.Token, record.Scene, "127.0.0.1"); err == nil || !raced {
		t.Fatal("a competing consumer after the read must prevent a second successful consume")
	}
}

func TestSMSConsumptionRejectsStateChangedAfterRead(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	record := entity.SMSVerificationCode{Phone: "13800000000", Scene: SMSSceneLogin, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute)}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	raced := false
	if err := db.Callback().Query().After("gorm:after_query").Register("simulate_competing_sms_consumer", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameSMSVerificationCode && !raced {
			raced = true
			if err := db.Exec("UPDATE sms_verification_codes SET status = ?, consumed_at = ? WHERE id = ?", SMSCodeStatusUsed, time.Now(), record.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if evidence, err := VerifySMSCodeDetailedInTx(db, record.Phone, record.Scene, "123456"); err == nil || evidence.SMSRecordID != 0 || !raced {
		t.Fatalf("a competing consumer after the read must prevent another evidence: %+v err=%v", evidence, err)
	}
}
