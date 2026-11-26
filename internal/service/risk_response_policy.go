package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	riskResponseAutoMaxMinutes   = 24 * 60
	riskResponseManualMaxMinutes = 365 * 24 * 60
)

var ErrLegacyRiskResponsePolicyReadOnly = errors.New("按类型共享的旧处置策略已停用，请在风控规则编辑器保存独立处置策略")

var ErrRiskResponseInvalid = errors.New("风控处置参数无效")

func invalidRiskResponse(message string) error {
	return fmt.Errorf("%w：%s", ErrRiskResponseInvalid, message)
}
func riskResponseDetector(kind string) bool {
	return kind == entity.RiskRuleIDLoginBruteforce || kind == entity.RiskRuleIDDeviceRegisterBurst
}

type RiskResponsePolicy struct {
	Mode             string `json:"mode"`
	DurationMinutes  int    `json:"durationMinutes"`
	BlacklistEnabled bool   `json:"blacklistEnabled"`
}

func validRiskResponsePolicy(mode string, minutes int) bool {
	return (mode == "notify" || mode == "temp_ip") && minutes >= 1 && minutes <= riskResponseAutoMaxMinutes
}
func GetRiskResponsePolicy(db *gorm.DB, detector string) (RiskResponsePolicy, error) {
	p := RiskResponsePolicy{Mode: "notify", DurationMinutes: 30}
	if !riskResponseDetector(detector) {
		return p, invalidRiskResponse("该检测器不支持网络处置")
	}
	setting, found, err := getSystemSettingByKey(db, "risk_response_"+detector)
	if err != nil {
		return p, err
	}
	if found {
		if err := json.Unmarshal([]byte(setting.Value), &p); err != nil || !validRiskResponsePolicy(p.Mode, p.DurationMinutes) {
			return RiskResponsePolicy{}, fmt.Errorf("风控处置策略配置损坏")
		}
	}
	p.BlacklistEnabled = GetBlacklistEnabledWithDB(db)
	return p, nil
}
func PutRiskResponsePolicy(db *gorm.DB, detector, mode string, minutes int, actorID int32) (RiskResponsePolicy, error) {
	if !riskResponseDetector(detector) || actorID <= 0 || !validRiskResponsePolicy(mode, minutes) {
		return RiskResponsePolicy{}, invalidRiskResponse("仅支持提醒或临时单 IP，自动期限为 1–1440 分钟")
	}
	p := RiskResponsePolicy{Mode: mode, DurationMinutes: minutes}
	err := db.Transaction(func(tx *gorm.DB) error {
		// The old endpoint is retained for archived configuration inspection only
		// after the one-time copy. Never silently accept a disconnected write.
		flag, found, err := getSystemSettingByKey(tx, RiskRuleResponseMigrationKey)
		if err != nil {
			return err
		}
		if found && flag.Value == "1" {
			return ErrLegacyRiskResponsePolicyReadOnly
		}
		// Never persist or change blacklist_enabled here.
		data, err := json.Marshal(map[string]any{"mode": mode, "durationMinutes": minutes})
		if err != nil {
			return err
		}
		if err := saveSystemSettingValue(tx, "risk_response_"+detector, string(data), "风控检测器处置策略（不改变黑名单总开关）"); err != nil {
			return err
		}
		return tx.Create(&entity.RiskRuleAudit{RuleID: detector, Action: "response_policy", ActorID: actorID, SnapshotJSON: string(data)}).Error
	})
	p.BlacklistEnabled = GetBlacklistEnabledWithDB(db)
	return p, err
}
