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
	Phone                string `json:"phone"`
	Scene                string `json:"scene"`
	RequestIP            string `json:"requestIp"`
	TargetUserID         int32  `json:"targetUserId"`
	CaptchaCorrelationID string `json:"-"`
}

type SMSVerificationEvidence struct {
	SMSRecordID          int32
	CaptchaCorrelationID string
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
	_, err := SendSMSCodeDetailedWithDB(db, input)
	return err
}

func SendSMSCodeDetailedWithDB(db *gorm.DB, input SendSMSCodeInput) (SMSVerificationEvidence, error) {
	input.Scene = strings.TrimSpace(input.Scene)
	phone, _, err := NormalizePhone(input.Phone)
	if err != nil {
		return SMSVerificationEvidence{}, err
	}
	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return SMSVerificationEvidence{}, err
	}
	if strings.TrimSpace(settings.SMSBaoUsername) == "" || strings.TrimSpace(settings.SMSBaoSecret) == "" {
		return SMSVerificationEvidence{}, fmt.Errorf("短信配置未完成")
	}
	if strings.TrimSpace(input.Scene) == SMSSceneLogin && !settings.SMSLoginEnabled {
		return SMSVerificationEvidence{}, fmt.Errorf("短信验证码登录未开启")
	}
	if strings.TrimSpace(input.Scene) == SMSSceneChangePhone && !settings.SMSLoginEnabled {
		return SMSVerificationEvidence{}, fmt.Errorf("手机号功能未启用")
	}
	if strings.TrimSpace(input.Scene) == SMSSceneRegister && !settings.RegisterEnabled {
		return SMSVerificationEvidence{}, fmt.Errorf("未开放注册")
	}

	if err := ensureSMSSceneAllowed(db, phone, input.Scene, input.TargetUserID); err != nil {
		return SMSVerificationEvidence{}, err
	}
	if err := ensureSMSCooldown(db, phone, input.Scene); err != nil {
		return SMSVerificationEvidence{}, err
	}

	code := generateSMSCode()
	provider := smsSender(settings)
	startedAt := smsCodeNow()
	if err := provider.SendSMS(phone, buildSMSCodeContent(settings, code)); err != nil {
		status, message := smsSendAuditFailure(err)
		writeSMSSendAudit(db, input, phone, settings.SMSProvider, SMSVerificationEvidence{}, status, message, smsCodeNow().Sub(startedAt))
		return SMSVerificationEvidence{}, fmt.Errorf("%s", message)
	}

	now := smsCodeNow()
	record := entity.SMSVerificationCode{
		Phone: phone, Scene: input.Scene, CodeHash: hashSMSCode(code),
		Status: SMSCodeStatusPending, ExpiresAt: now.Add(smsCodeTTL),
		RequestIP: strings.TrimSpace(input.RequestIP), CaptchaChallengeID: strings.TrimSpace(input.CaptchaCorrelationID),
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := smsVerificationDB(tx).Where("phone = ? AND scene = ? AND status = ?", phone, input.Scene, SMSCodeStatusPending).Update("status", SMSCodeStatusCanceled).Error; err != nil {
			return err
		}
		return smsVerificationDB(tx).Create(&record).Error
	}); err != nil {
		writeSMSSendAudit(db, input, phone, settings.SMSProvider, SMSVerificationEvidence{}, "pending", "短信已发送，但验证码记录保存失败", smsCodeNow().Sub(startedAt))
		return SMSVerificationEvidence{}, err
	}
	evidence := SMSVerificationEvidence{SMSRecordID: record.ID, CaptchaCorrelationID: record.CaptchaChallengeID}
	writeSMSSendAudit(db, input, phone, settings.SMSProvider, evidence, "success", "短信发送成功", smsCodeNow().Sub(startedAt))
	return evidence, nil
}

func smsSendAuditFailure(err error) (string, string) {
	// Unknown transport results must not claim that the provider rejected a send.
	switch err.Error() {
	case "短信宝鉴权失败", "短信宝账号不存在", "短信宝余额不足", "短信宝 IP 受限", "短信内容含敏感词", "手机号格式错误", "当前短信配置仅允许发送国内手机号":
		return "failed", err.Error()
	default:
		return "pending", "短信发送结果待确认，请勿重复发送"
	}
}

func writeSMSSendAudit(db *gorm.DB, input SendSMSCodeInput, phone string, provider string, evidence SMSVerificationEvidence, status string, message string, duration time.Duration) {
	correlationID, username := "", ""
	if evidence.SMSRecordID > 0 {
		correlationID = fmt.Sprintf("sms:%d", evidence.SMSRecordID)
	}
	if input.TargetUserID > 0 {
		var user entity.User
		if err := db.Session(&gorm.Session{NewDB: true}).Select("username").First(&user, input.TargetUserID).Error; err == nil {
			username = user.Username
		}
	}
	_ = WriteAccountAuditWithDB(db, AccountAuditEvent{
		UserID: input.TargetUserID, Username: username, Module: "sms", Action: 30,
		Description: message, ClientIP: strings.TrimSpace(input.RequestIP), CorrelationID: correlationID,
		Request: map[string]any{"scene": input.Scene, "phone": MaskAuditPhone(phone), "provider": provider},
		Result:  map[string]any{"status": status, "message": message, "durationMs": duration.Milliseconds(), "smsRecordId": evidence.SMSRecordID},
	})
}

func VerifySMSCodeWithDB(db *gorm.DB, rawPhone string, scene string, smsCode string) error {
	_, err := VerifySMSCodeDetailedWithDB(db, rawPhone, scene, smsCode)
	return err
}

func VerifySMSCodeInTx(db *gorm.DB, rawPhone string, scene string, smsCode string) error {
	_, err := VerifySMSCodeDetailedInTx(db, rawPhone, scene, smsCode)
	return err
}

func VerifySMSCodeDetailedWithDB(db *gorm.DB, rawPhone string, scene string, smsCode string) (SMSVerificationEvidence, error) {
	var evidence SMSVerificationEvidence
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		evidence, err = VerifySMSCodeDetailedInTx(tx, rawPhone, scene, smsCode)
		return err
	})
	if err != nil {
		return SMSVerificationEvidence{}, err
	}
	return evidence, nil
}

// VerifySMSCodeDetailedInTx returns the exact consumed SMS row, so a later
// successful authentication never infers ownership from phone, IP or time.
func VerifySMSCodeDetailedInTx(db *gorm.DB, rawPhone string, scene string, smsCode string) (SMSVerificationEvidence, error) {
	phone, _, err := NormalizePhone(rawPhone)
	if err != nil {
		return SMSVerificationEvidence{}, err
	}
	verificationDB := smsVerificationDB(db)
	var record entity.SMSVerificationCode
	if err := verificationDB.Where("phone = ? AND scene = ? AND status = ?", phone, scene, SMSCodeStatusPending).
		Order("id desc").
		First(&record).Error; err != nil {
		return SMSVerificationEvidence{}, fmt.Errorf("验证码错误或已过期")
	}
	if smsCodeNow().After(record.ExpiresAt) {
		_ = smsVerificationDB(db).Model(&record).Update("status", SMSCodeStatusExpired).Error
		return SMSVerificationEvidence{}, fmt.Errorf("验证码错误或已过期")
	}
	if record.CodeHash != hashSMSCode(smsCode) {
		return SMSVerificationEvidence{}, fmt.Errorf("验证码错误或已过期")
	}
	now := smsCodeNow()
	update := smsVerificationDB(db).Where("id = ? AND status = ?", record.ID, SMSCodeStatusPending).Updates(map[string]any{
		"status":      SMSCodeStatusUsed,
		"consumed_at": &now,
	})
	if update.Error != nil {
		return SMSVerificationEvidence{}, update.Error
	}
	if update.RowsAffected != 1 {
		return SMSVerificationEvidence{}, fmt.Errorf("验证码错误或已过期")
	}
	return SMSVerificationEvidence{SMSRecordID: record.ID, CaptchaCorrelationID: strings.TrimSpace(record.CaptchaChallengeID)}, nil
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
