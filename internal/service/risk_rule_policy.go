package service

import (
	"encoding/json"
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const RiskRuleResponseMigrationKey = "risk_rule_response_policy_v1"
const (
	riskRuleNotify    = "notify"
	riskRuleTemporary = "temporary"
	riskRulePermanent = "permanent"
)

type RiskRuleResponsePolicy struct {
	Mode            string `json:"mode"`
	Scope           string `json:"scope,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
}

// Confirmation is a request precondition, not a saved switch or an audit value.
type RiskRuleResponseInput struct {
	RiskRuleResponsePolicy
	Confirmed bool `json:"confirmed,omitempty"`
}

func validateRuleResponse(kind string, p RiskRuleResponsePolicy) bool {
	if p.Mode == riskRuleNotify {
		return p.Scope == "" && p.DurationMinutes == 0
	}
	if !riskResponseDetector(kind) || (p.Scope != "ip" && p.Scope != "segment") {
		return false
	}
	switch p.Mode {
	case riskRuleTemporary:
		return p.DurationMinutes > 0 && p.DurationMinutes <= riskResponseAutoMaxMinutes
	case riskRulePermanent:
		return p.DurationMinutes == 0
	default:
		return false
	}
}

func encodeRuleResponse(kind string, input *RiskRuleResponseInput, current string) (string, error) {
	if input == nil {
		// Older metadata-only clients cannot reset or copy a rule's response by omission.
		return current, nil
	}
	p := input.RiskRuleResponsePolicy
	if !validateRuleResponse(kind, p) {
		return "", fmt.Errorf("%w：处置策略、范围或期限无效（临时封禁为 1–1440 分钟）", ErrRiskRuleInvalid)
	}
	if (p.Mode == riskRulePermanent || p.Scope == "segment") && !input.Confirmed {
		return "", fmt.Errorf("%w：永久封禁或 IP 段封禁必须明确确认", ErrRiskRuleInvalid)
	}
	data, err := json.Marshal(p)
	return string(data), err
}

func resolveRuleResponse(row entity.RiskRule) RiskRuleResponsePolicy {
	var p RiskRuleResponsePolicy
	if json.Unmarshal([]byte(row.ResponsePolicyJSON), &p) == nil && validateRuleResponse(effectiveRiskRuleType(row), p) {
		return p
	}
	return RiskRuleResponsePolicy{Mode: riskRuleNotify}
}

// MigrateLegacyRiskRulePolicies snapshots legacy opt-in policies for pre-existing rules.
// Never expand the old eligibility conditions, reapply old warnings, or modify an explicit policy.
func MigrateLegacyRiskRulePolicies(tx *gorm.DB) error {
	for _, kind := range []string{entity.RiskRuleIDLoginBruteforce, entity.RiskRuleIDDeviceRegisterBurst} {
		setting, found, err := getSystemSettingByKey(tx, "risk_response_"+kind)
		if err != nil {
			return err
		}
		var legacy RiskResponsePolicy
		if !found || json.Unmarshal([]byte(setting.Value), &legacy) != nil ||
			legacy.Mode != "temp_ip" || !validRiskResponsePolicy(legacy.Mode, legacy.DurationMinutes) {
			continue
		}
		p := RiskRuleResponsePolicy{Mode: riskRuleTemporary, Scope: "ip", DurationMinutes: legacy.DurationMinutes}
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := tx.Model(&entity.RiskRule{}).
			Where("deleted_at IS NULL AND response_policy_json = '' AND (rule_type = ? OR (rule_type = '' AND rule_id = ?))", kind, kind).
			UpdateColumn("response_policy_json", string(data)).Error; err != nil {
			return err
		}
	}
	return nil
}
