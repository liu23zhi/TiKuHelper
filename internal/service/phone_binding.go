package service

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var mainlandPhonePattern = regexp.MustCompile(`^1\d{10}$`)

func NormalizeMainlandPhone(value string) (string, error) {
	normalized := strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(value))
	if !mainlandPhonePattern.MatchString(normalized) {
		return "", fmt.Errorf("手机号格式不正确")
	}
	return normalized, nil
}

func MaskPhone(value string) string {
	if len(value) != 11 {
		return value
	}
	return value[:3] + "****" + value[7:]
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
			return tx.Save(&updated).Error
		}

		phone, err := NormalizeMainlandPhone(trimmed)
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
		return tx.Save(&updated).Error
	})
	return updated, err
}
