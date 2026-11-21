package service

import (
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type riskDetectorFunc func(*gorm.DB, entity.RiskRule, time.Time) ([]riskWarningCandidate, error)

// riskDetectors is the complete server-side allowlist. Never dispatch user-supplied code.
var riskDetectors = map[string]riskDetectorFunc{
	entity.RiskRuleIDDebtUser:                collectUserDebtCandidates,
	entity.RiskRuleIDDebtBatch:               collectBatchDebtCandidates,
	entity.RiskRuleIDDebtSystem:              collectSystemDebtCandidates,
	entity.RiskRuleIDMultiAccountIP:          collectMultiAccountIPCandidates,
	entity.RiskRuleIDTicketFlood:             collectTicketFloodCandidates,
	entity.RiskRuleIDAbnormalRegisterPattern: collectAbnormalRegisterPatternCandidates,
	entity.RiskRuleIDTokenAbuse:              collectTokenAbuseCandidates,
	entity.RiskRuleIDOffhoursActivity:        collectOffhoursActivityCandidates,
	entity.RiskRuleIDBindingChurn:            collectBindingChurnCandidates,
	entity.RiskRuleIDWechatPhoneShare:        collectWechatPhoneShareCandidates,
	entity.RiskRuleIDImpossibleTravel:        collectImpossibleTravelCandidates,
	entity.RiskRuleIDCredentialStuffing:      collectCredentialStuffingCandidates,
	entity.RiskRuleIDLoginBruteforce:         collectLoginBruteforceCandidates,
	entity.RiskRuleIDRefundAbuse:             collectRefundAbuseCandidates,
	entity.RiskRuleIDProxyIPLogin:            collectProxyIPLoginCandidates,
	entity.RiskRuleIDRegisterBurst:           collectRegisterBurstCandidates,
	entity.RiskRuleIDAnswerPatternRepeat:     collectAnswerPatternRepeatCandidates,
}

func riskDetectorByType(kind string) (riskDetectorFunc, bool) {
	detector, ok := riskDetectors[kind]
	return detector, ok
}

func riskDetectorRegistered(kind string) bool {
	_, ok := riskDetectors[kind]
	return ok
}

func effectiveRiskRuleType(rule entity.RiskRule) string {
	if rule.Type != "" {
		return rule.Type
	}
	return rule.RuleID
}
