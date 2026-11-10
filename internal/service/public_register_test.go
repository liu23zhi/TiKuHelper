package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newPublicRegisterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:public-register-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.BillingRecord{}, &entity.CaptchaToken{}, &entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedPublicRegisterCredentials(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&entity.CaptchaToken{
		Token:       "captcha-token",
		Scene:       CaptchaSceneRegisterSubmit,
		ChallengeID: "challenge-1",
		Status:      CaptchaTokenStatusActive,
		ExpiresAt:   now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed captcha token: %v", err)
	}
	if err := db.Create(&entity.SMSVerificationCode{
		Phone:     "13800000000",
		Scene:     SMSSceneRegister,
		CodeHash:  hashSMSCode("123456"),
		Status:    SMSCodeStatusPending,
		ExpiresAt: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed sms code: %v", err)
	}
}

func TestRegisterTxComposesCredentialConsumptionAndUserBonus(t *testing.T) {
	db := newPublicRegisterTestDB(t)
	seedPublicRegisterCredentials(t, db)
	phone := "13800000000"
	user := &entity.User{Username: "register_user", Password: "password", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeCaptchaTokenInTx(tx, "captcha-token", CaptchaSceneRegisterSubmit, "127.0.0.1"); err != nil {
			return err
		}
		if err := VerifySMSCodeInTx(tx, phone, SMSSceneRegister, "123456"); err != nil {
			return err
		}
		return CreateUserWithNewUserBonusInTx(tx, user, NewUserBonusOptions{Enabled: true, Amount: 1.25, Source: RegistrationBonusSourcePublicRegister})
	})
	if err != nil {
		t.Fatalf("register transaction: %v", err)
	}

	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.GiftBalance != 1.25 {
		t.Fatalf("expected bonus in gift_balance 1.25, got %v", stored.GiftBalance)
	}
	// 双桶模型：注册赠送进赠送余额（可消费、不可退），不进可退本金。
	if stored.Balance != 0 {
		t.Fatalf("registration bonus must not enter the refundable principal, got %v", stored.Balance)
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ?", user.ID, registrationBonusBillingRecordType).Count(&count).Error; err != nil {
		t.Fatalf("count bonus ledger: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one bonus ledger, got %d", count)
	}
}

func TestRegisterTxRollbackLeavesCaptchaAndSMSRetryable(t *testing.T) {
	db := newPublicRegisterTestDB(t)
	seedPublicRegisterCredentials(t, db)
	boundWechatID := "wx-existing"
	if err := db.Create(&entity.User{Username: "existing", Password: "password", Role: "user", Status: "enabled", RegisterSource: "public", WechatID: &boundWechatID}).Error; err != nil {
		t.Fatalf("seed existing user: %v", err)
	}
	phone := "13800000000"
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeCaptchaTokenInTx(tx, "captcha-token", CaptchaSceneRegisterSubmit, "127.0.0.1"); err != nil {
			return err
		}
		if err := VerifySMSCodeInTx(tx, phone, SMSSceneRegister, "123456"); err != nil {
			return err
		}
		user := &entity.User{Username: "duplicate_wechat", Password: "password", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone, WechatID: &boundWechatID}
		return CreateUserWithNewUserBonusInTx(tx, user, NewUserBonusOptions{Enabled: true, Amount: 1, Source: RegistrationBonusSourcePublicRegister})
	})
	if err == nil {
		t.Fatal("expected duplicate wechat id to roll back")
	}

	var captcha entity.CaptchaToken
	if err := db.First(&captcha, "token = ?", "captcha-token").Error; err != nil {
		t.Fatalf("load captcha token: %v", err)
	}
	if captcha.Status != CaptchaTokenStatusActive {
		t.Fatalf("expected captcha token active after rollback, got %s", captcha.Status)
	}
	var sms entity.SMSVerificationCode
	if err := db.First(&sms, "phone = ? AND scene = ?", phone, SMSSceneRegister).Error; err != nil {
		t.Fatalf("load sms code: %v", err)
	}
	if sms.Status != SMSCodeStatusPending {
		t.Fatalf("expected sms code pending after rollback, got %s", sms.Status)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeCaptchaTokenInTx(tx, "captcha-token", CaptchaSceneRegisterSubmit, "127.0.0.1"); err != nil {
			return err
		}
		return VerifySMSCodeInTx(tx, phone, SMSSceneRegister, "123456")
	}); err != nil {
		t.Fatalf("retry credentials after rollback: %v", err)
	}
}

func TestRegisterTxBonusLedgerFailureRollsBackUserAndCredentials(t *testing.T) {
	db := newPublicRegisterTestDB(t)
	seedPublicRegisterCredentials(t, db)
	if err := db.Exec(`CREATE TRIGGER fail_registration_bonus BEFORE INSERT ON billing_records WHEN NEW.type = 'registration_bonus' BEGIN SELECT RAISE(ABORT, 'bonus ledger failed'); END`).Error; err != nil {
		t.Fatalf("create bonus failure trigger: %v", err)
	}
	phone := "13800000000"
	user := &entity.User{Username: "bonus_failure", Password: "password", Role: "user", Status: "enabled", RegisterSource: "public", Phone: &phone}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeCaptchaTokenInTx(tx, "captcha-token", CaptchaSceneRegisterSubmit, "127.0.0.1"); err != nil {
			return err
		}
		if err := VerifySMSCodeInTx(tx, phone, SMSSceneRegister, "123456"); err != nil {
			return err
		}
		return CreateUserWithNewUserBonusInTx(tx, user, NewUserBonusOptions{Enabled: true, Amount: 1, Source: RegistrationBonusSourcePublicRegister})
	})
	if err == nil {
		t.Fatal("expected bonus ledger failure")
	}
	var count int64
	if err := db.Model(&entity.User{}).Where("username = ?", user.Username).Count(&count).Error; err != nil {
		t.Fatalf("count rolled back user: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no user after bonus failure, got %d", count)
	}
	if err := db.First(&entity.CaptchaToken{}, "token = ? AND status = ?", "captcha-token", CaptchaTokenStatusActive).Error; err != nil {
		t.Fatalf("expected captcha retryability after bonus failure: %v", err)
	}
	if err := db.First(&entity.SMSVerificationCode{}, "phone = ? AND status = ?", phone, SMSCodeStatusPending).Error; err != nil {
		t.Fatalf("expected sms retryability after bonus failure: %v", err)
	}
}
