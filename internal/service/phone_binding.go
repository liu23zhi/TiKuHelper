package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const PhoneRegionMainland = "mainland"
const PhoneRegionInternational = "international"

var ErrPhoneAlreadyBound = errors.New("该手机号已被绑定")
var ErrPhoneBindingConflict = errors.New("该手机号存在历史绑定冲突，请联系管理员处理")

var mainlandPhonePattern = regexp.MustCompile(`^1\d{10}$`)
var internationalPhonePattern = regexp.MustCompile(`^\+[1-9]\d{6,18}$`)

func normalizePhoneText(value string) string {
	normalized := strings.NewReplacer(" ", "", "-", "", "\t", "", "\r", "", "\n", "").Replace(strings.TrimSpace(value))
	return normalized
}

// NormalizeMainlandPhoneInput accepts the current 11-digit form and the
// explicitly supported legacy +86 form, returning the canonical digits-only
// mainland number. Other country codes, letters, and overlong values are
// rejected rather than being partially stripped into another number.
func NormalizeMainlandPhoneInput(value string) (string, error) {
	normalized := normalizePhoneText(value)
	if strings.HasPrefix(normalized, "+86") {
		normalized = normalized[len("+86"):]
	}
	if !mainlandPhonePattern.MatchString(normalized) {
		return "", fmt.Errorf("手机号格式不正确")
	}
	return normalized, nil
}

func NormalizePhone(value string) (string, string, error) {
	normalized, err := NormalizeMainlandPhoneInput(value)
	if err != nil {
		return "", "", err
	}
	return normalized, PhoneRegionMainland, nil
}

func NormalizeMainlandPhone(value string) (string, error) {
	return NormalizeMainlandPhoneInput(value)
}

// PhoneLookupCandidates returns exact historical storage forms for a
// canonical mainland phone. The canonical form is always first so callers can
// preserve the existing value when no change is needed.
func PhoneLookupCandidates(canonical string) []string {
	normalized, err := NormalizeMainlandPhoneInput(canonical)
	if err != nil {
		return nil
	}
	return []string{normalized, "+86" + normalized}
}

// FindUsersByPhoneAliases finds every account using either the current or the
// legacy +86 storage form. Callers must treat more than one result as an
// explicit historical conflict; never select an arbitrary first row.
func FindUsersByPhoneAliases(db *gorm.DB, canonical string) ([]entity.User, error) {
	aliases := PhoneLookupCandidates(canonical)
	if len(aliases) == 0 {
		return nil, fmt.Errorf("手机号格式不正确")
	}
	var users []entity.User
	if err := db.Where("phone IN ?", aliases).Order("id asc").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func MaskPhone(value string) string {
	normalized, err := NormalizeMainlandPhoneInput(value)
	if err == nil {
		return normalized[:3] + "****" + normalized[7:]
	}
	// Keep legacy non-mainland values masked even though new account input is
	// restricted to mainland numbers.
	normalized = strings.TrimSpace(value)
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
			return tx.Model(&entity.User{}).Where("id = ?", targetUserID).
				Select("phone", "phone_bound_at").Updates(&updated).Error
		}

		phone, _, err := NormalizePhone(trimmed)
		if err != nil {
			return err
		}

		users, findErr := FindUsersByPhoneAliases(tx, phone)
		if findErr != nil {
			return findErr
		}
		if len(users) > 1 {
			return ErrPhoneBindingConflict
		}
		if updated.Phone != nil {
			for _, alias := range PhoneLookupCandidates(phone) {
				if *updated.Phone == alias {
					return nil
				}
			}
		}
		occupied := make([]entity.User, 0, len(users))
		for _, user := range users {
			if user.ID != targetUserID {
				occupied = append(occupied, user)
			}
		}
		if len(occupied) > 0 && !forceReassign {
			return ErrPhoneAlreadyBound
		}
		if len(occupied) == 1 && forceReassign {
			if err := tx.Model(&entity.User{}).Where("id = ?", occupied[0].ID).Updates(map[string]any{
				"phone":          nil,
				"phone_bound_at": nil,
			}).Error; err != nil {
				return err
			}
		}

		now := time.Now()
		updated.Phone = &phone
		updated.PhoneBoundAt = &now
		return tx.Model(&entity.User{}).Where("id = ?", targetUserID).
			Select("phone", "phone_bound_at").Updates(&updated).Error
	})
	return updated, err
}

func UpdateUserPhoneWithVerificationWithDB(db *gorm.DB, targetUserID int32, rawPhone string, smsCode string) (entity.User, error) {
	phone, _, err := NormalizePhone(rawPhone)
	if err != nil {
		return entity.User{}, err
	}

	var updated entity.User
	err = db.Transaction(func(tx *gorm.DB) error {
		var current entity.User
		if err := tx.Where("id = ?", targetUserID).First(&current).Error; err != nil {
			return err
		}
		users, err := FindUsersByPhoneAliases(tx, phone)
		if err != nil {
			return err
		}
		if len(users) > 1 {
			return ErrPhoneBindingConflict
		}
		if current.Phone != nil {
			for _, alias := range PhoneLookupCandidates(phone) {
				if *current.Phone == alias {
					return fmt.Errorf("新手机号不能与当前手机号相同")
				}
			}
		}
		for _, user := range users {
			if user.ID != targetUserID {
				return ErrPhoneAlreadyBound
			}
		}
		if err := VerifySMSCodeInTx(tx, phone, SMSSceneChangePhone, smsCode); err != nil {
			return err
		}
		updated, err = UpdateUserPhoneWithDB(tx, targetUserID, phone, false)
		return err
	})
	return updated, err
}
