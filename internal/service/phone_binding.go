package service

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const PhoneRegionMainland = "mainland"
const PhoneRegionInternational = "international"

var mainlandPhonePattern = regexp.MustCompile(`^1\d{10}$`)
var internationalPhonePattern = regexp.MustCompile(`^\+[1-9]\d{6,18}$`)

func normalizePhoneText(value string) string {
	normalized := strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(value))
	return normalized
}

func NormalizePhone(value string) (string, string, error) {
	normalized := normalizePhoneText(value)
	if mainlandPhonePattern.MatchString(normalized) {
		return normalized, PhoneRegionMainland, nil
	}
	if internationalPhonePattern.MatchString(normalized) {
		return normalized, PhoneRegionInternational, nil
	}
	return "", "", fmt.Errorf("手机号格式不正确")
}

func NormalizeMainlandPhone(value string) (string, error) {
	normalized := normalizePhoneText(value)
	if !mainlandPhonePattern.MatchString(normalized) {
		return "", fmt.Errorf("手机号格式不正确")
	}
	return normalized, nil
}

func MaskPhone(value string) string {
	normalized := strings.TrimSpace(value)
	if mainlandPhonePattern.MatchString(normalized) {
		return normalized[:3] + "****" + normalized[7:]
	}
	if internationalPhonePattern.MatchString(normalized) && len(normalized) > 8 {
		return normalized[:4] + "****" + normalized[len(normalized)-4:]
	}
	return value
}

func UpdateUserPhoneWithDB(db *gorm.DB, targetUserID int32, rawPhone string, forceReassign bool) (entity.User, error) {
	var updated entity.User
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", targetUserID).First(&updated).Error; err != nil {
			return err
		}

		trimmed := strings.TrimSpace(rawPhone)
		if trimmed == "" {
			updated.Phone = nil
			updated.PhoneBoundAt = nil
			return tx.Select("phone", "phone_bound_at").Updates(&updated).Error
		}

		phone, _, err := NormalizePhone(trimmed)
		if err != nil {
			return err
		}

		var occupied entity.User
		err = tx.Where("phone = ? AND id <> ?", phone, targetUserID).First(&occupied).Error
		if err == nil && !forceReassign {
			return fmt.Errorf("该手机号已被绑定")
		}
		if err == nil && forceReassign {
			if err := tx.Model(&entity.User{}).Where("id = ?", occupied.ID).Updates(map[string]any{
				"phone":          nil,
				"phone_bound_at": nil,
			}).Error; err != nil {
				return err
			}
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}

		now := time.Now()
		updated.Phone = &phone
		updated.PhoneBoundAt = &now
		return tx.Select("phone", "phone_bound_at").Updates(&updated).Error
	})
	return updated, err
}

func UpdateUserPhoneWithVerificationWithDB(db *gorm.DB, targetUserID int32, rawPhone string, smsCode string) (entity.User, error) {
	phone, _, err := NormalizePhone(rawPhone)
	if err != nil {
		return entity.User{}, err
	}

	var current entity.User
	if err := db.Where("id = ?", targetUserID).First(&current).Error; err != nil {
		return entity.User{}, err
	}
	if current.Phone != nil && *current.Phone == phone {
		return entity.User{}, fmt.Errorf("新手机号不能与当前手机号相同")
	}

	var occupied int64
	if err := db.Model(&entity.User{}).
		Where("phone = ? AND id <> ?", phone, targetUserID).
		Count(&occupied).Error; err != nil {
		return entity.User{}, err
	}
	if occupied > 0 {
		return entity.User{}, fmt.Errorf("该手机号已被绑定")
	}
	if err := VerifySMSCodeWithDB(db, phone, SMSSceneChangePhone, smsCode); err != nil {
		return entity.User{}, err
	}
	return UpdateUserPhoneWithDB(db, targetUserID, phone, false)
}
