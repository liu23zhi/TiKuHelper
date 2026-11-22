package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// GatewayCertificatePasswordsSettingKey is the system_settings key read by the
// container gateway. The value is a JSON array of plaintext password candidates.
// The administrator endpoint never returns the value; it only returns metadata.
const GatewayCertificatePasswordsSettingKey = "gateway_certificate_passwords"

const (
	MaxGatewayCertificatePasswordCandidates = 32
	MaxGatewayCertificatePasswordBytes      = 4096
)

var (
	ErrTooManyGatewayCertificatePasswords = errors.New("证书密码候选最多只能保存 32 条")
	ErrGatewayCertificatePasswordTooLong  = errors.New("单条证书密码不能超过 4096 字节")
)

type GatewayCertificatePasswordState struct {
	Count     int        `json:"count"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// NormalizeGatewayCertificatePasswords trims only the line terminator and
// removes blank/duplicate entries. Spaces inside a password remain significant.
func NormalizeGatewayCertificatePasswords(passwords []string) ([]string, error) {
	result := make([]string, 0, len(passwords))
	seen := make(map[string]struct{}, len(passwords))
	for _, raw := range passwords {
		password := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(password) == "" {
			continue
		}
		if len([]byte(password)) > MaxGatewayCertificatePasswordBytes {
			return nil, ErrGatewayCertificatePasswordTooLong
		}
		if _, exists := seen[password]; exists {
			continue
		}
		seen[password] = struct{}{}
		result = append(result, password)
		if len(result) > MaxGatewayCertificatePasswordCandidates {
			return nil, ErrTooManyGatewayCertificatePasswords
		}
	}
	return result, nil
}

func GetGatewayCertificatePasswordStateWithDB(db *gorm.DB) (GatewayCertificatePasswordState, error) {
	if db == nil {
		return GatewayCertificatePasswordState{}, errors.New("数据库未初始化")
	}
	setting, exists, err := getSystemSettingByKey(db, GatewayCertificatePasswordsSettingKey)
	if err != nil {
		return GatewayCertificatePasswordState{}, err
	}
	if !exists || strings.TrimSpace(setting.Value) == "" {
		return GatewayCertificatePasswordState{}, nil
	}
	var passwords []string
	if err := json.Unmarshal([]byte(setting.Value), &passwords); err != nil {
		return GatewayCertificatePasswordState{}, fmt.Errorf("证书密码候选配置损坏: %w", err)
	}
	normalized, err := NormalizeGatewayCertificatePasswords(passwords)
	if err != nil {
		return GatewayCertificatePasswordState{}, fmt.Errorf("证书密码候选配置损坏: %w", err)
	}
	state := GatewayCertificatePasswordState{Count: len(normalized)}
	if !setting.UpdatedAt.IsZero() {
		updated := setting.UpdatedAt
		state.UpdatedAt = &updated
	}
	return state, nil
}

// GetGatewayCertificatePasswordsWithDB is intentionally an internal-facing
// reader for the co-located container gateway. HTTP handlers must use the
// metadata-only function above.
func GetGatewayCertificatePasswordsWithDB(db *gorm.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("数据库未初始化")
	}
	setting, exists, err := getSystemSettingByKey(db, GatewayCertificatePasswordsSettingKey)
	if err != nil || !exists || strings.TrimSpace(setting.Value) == "" {
		return []string{}, err
	}
	var passwords []string
	if err := json.Unmarshal([]byte(setting.Value), &passwords); err != nil {
		return nil, fmt.Errorf("证书密码候选配置损坏: %w", err)
	}
	return NormalizeGatewayCertificatePasswords(passwords)
}

func SetGatewayCertificatePasswordsWithDB(db *gorm.DB, passwords []string) (GatewayCertificatePasswordState, error) {
	if db == nil {
		return GatewayCertificatePasswordState{}, errors.New("数据库未初始化")
	}
	normalized, err := NormalizeGatewayCertificatePasswords(passwords)
	if err != nil {
		return GatewayCertificatePasswordState{}, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return GatewayCertificatePasswordState{}, err
	}
	if err := saveSystemSettingValue(db, GatewayCertificatePasswordsSettingKey, string(payload), "Nginx 证书密码候选（仅网关读取）"); err != nil {
		return GatewayCertificatePasswordState{}, err
	}
	return GetGatewayCertificatePasswordStateWithDB(db)
}
