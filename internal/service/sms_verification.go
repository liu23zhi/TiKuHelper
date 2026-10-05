package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const SMSSceneRegister = "register"
const SMSSceneLogin = "login"
const SMSSceneChangePhone = "change_phone"
const SMSCodeStatusPending = "pending"
const SMSCodeStatusUsed = "used"
const SMSCodeStatusExpired = "expired"
const SMSCodeStatusCanceled = "canceled"

const smsCodeTTL = 5 * time.Minute
const smsSendCooldown = 60 * time.Second

var smsSender = newSMSProviderFromSettings
var smsCodeNow = time.Now
var generateSMSCode = func() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

type SendSMSCodeInput struct {
	Phone        string `json:"phone"`
	Scene        string `json:"scene"`
	RequestIP    string `json:"requestIp"`
	TargetUserID int32  `json:"targetUserId"`
}

func hashSMSCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func buildSMSCodeContent(settings SystemSettingDetail, code string) string {
	template := strings.TrimSpace(settings.SMSCodeTemplate)
	if template == "" {
		template = DefaultSMSCodeTemplate
	}
	content := strings.ReplaceAll(template, "{sign}", strings.TrimSpace(settings.SMSCodeSignName))
	content = strings.ReplaceAll(content, "{code}", code)
	return content
}

func smsVerificationDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.SMSVerificationCode{})
}

func SendSMSCodeWithDB(db *gorm.DB, input SendSMSCodeInput) error {
	phone, _, err := NormalizePhone(input.Phone)
	if err != nil {
		return err
	}
	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return err
	}
	if strings.TrimSpace(settings.SMSBaoUsername) == "" || strings.TrimSpace(settings.SMSBaoSecret) == "" {
		return fmt.Errorf("短信配置未完成")
	}
	if strings.TrimSpace(input.Scene) == SMSSceneLogin && !settings.SMSLoginEnabled {
		return fmt.Errorf("短信验证码登录未开启")
	}
	if strings.TrimSpace(input.Scene) == SMSSceneRegister && !settings.RegisterEnabled {
		return fmt.Errorf("未开放注册")
	}

	if err := ensureSMSSceneAllowed(db, phone, input.Scene, input.TargetUserID); err != nil {
		return err
	}
	if err := ensureSMSCooldown(db, phone, input.Scene); err != nil {
		return err
	}

	verificationDB := smsVerificationDB(db)
	code := generateSMSCode()
	provider := smsSender(settings)
	if err := provider.SendSMS(phone, buildSMSCodeContent(settings, code)); err != nil {
		return err
	}

	now := smsCodeNow()
	_ = verificationDB.
		Where("phone = ? AND scene = ? AND status = ?", phone, input.Scene, SMSCodeStatusPending).
		Update("status", SMSCodeStatusCanceled).Error

	record := entity.SMSVerificationCode{
		Phone:     phone,
		Scene:     input.Scene,
		CodeHash:  hashSMSCode(code),
		Status:    SMSCodeStatusPending,
		ExpiresAt: now.Add(smsCodeTTL),
		RequestIP: strings.TrimSpace(input.RequestIP),
	}
	return verificationDB.Create(&record).Error
}

func VerifySMSCodeWithDB(db *gorm.DB, rawPhone string, scene string, smsCode string) error {
	phone, _, err := NormalizePhone(rawPhone)
	if err != nil {
		return err
	}
	verificationDB := smsVerificationDB(db)
	var record entity.SMSVerificationCode
	if err := verificationDB.Where("phone = ? AND scene = ? AND status = ?", phone, scene, SMSCodeStatusPending).
		Order("id desc").
		First(&record).Error; err != nil {
		return fmt.Errorf("验证码错误或已过期")
	}
	if smsCodeNow().After(record.ExpiresAt) {
		_ = smsVerificationDB(db).Model(&record).Update("status", SMSCodeStatusExpired).Error
		return fmt.Errorf("验证码错误或已过期")
	}
	if record.CodeHash != hashSMSCode(smsCode) {
		return fmt.Errorf("验证码错误或已过期")
	}
	now := smsCodeNow()
	return smsVerificationDB(db).Model(&record).Updates(map[string]any{
		"status":      SMSCodeStatusUsed,
		"consumed_at": &now,
	}).Error
}

func ensureSMSSceneAllowed(db *gorm.DB, phone string, scene string, targetUserID int32) error {
	switch strings.TrimSpace(scene) {
	case SMSSceneRegister:
		var count int64
		if err := db.Model(&entity.User{}).Where("phone = ?", phone).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("该手机号已被绑定")
		}
		return nil
	case SMSSceneLogin:
		var user entity.User
		if err := db.Where("phone = ?", phone).First(&user).Error; err != nil {
			return fmt.Errorf("该手机号未绑定账号，请先注册")
		}
		if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return fmt.Errorf("用户已被禁用")
		}
		return nil
	case SMSSceneChangePhone:
		if targetUserID <= 0 {
			return fmt.Errorf("换绑手机号用户无效")
		}
		var occupied int64
		if err := db.Model(&entity.User{}).
			Where("phone = ? AND id <> ?", phone, targetUserID).
			Count(&occupied).Error; err != nil {
			return err
		}
		if occupied > 0 {
			return fmt.Errorf("该手机号已被绑定")
		}
		return nil
	default:
		return fmt.Errorf("短信场景不正确")
	}
}

func ensureSMSCooldown(db *gorm.DB, phone string, scene string) error {
	verificationDB := smsVerificationDB(db)
	var latest entity.SMSVerificationCode
	err := verificationDB.Where("phone = ? AND scene = ?", phone, scene).Order("id desc").First(&latest).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	if smsCodeNow().Sub(latest.CreatedAt) < smsSendCooldown {
		return fmt.Errorf("发送过于频繁，请稍后再试")
	}
	return nil
}
