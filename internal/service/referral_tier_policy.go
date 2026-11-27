package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReferralAutoRulesSettingKey           = "referral_auto_rebate_rules"
	referralTierRebatePolicyVersion       = "tier-v1"
	referralRateScale               int64 = 100_000_000
	maxReferralRuleCents                  = maxEffectiveMicros / ticketRefundMicrosPerCent
)

var ErrReferralAutoRulesInvalid = errors.New("自动返佣规则无效")

// Array order is descending priority. LowerCents is inclusive, UpperCents
// exclusive. A nil upper limit is unbounded. RateUnits is the fractional rate
// in 1e-8 units (10% = 10000000), not a percentage or binary float.
type ReferralAutoRule struct {
	Enabled    bool   `json:"enabled"`
	LowerCents int64  `json:"lowerCents"`
	UpperCents *int64 `json:"upperCents"`
	RateUnits  int64  `json:"rateUnits"`
}
type ReferralAutoRules struct {
	Enabled bool               `json:"enabled"`
	Rules   []ReferralAutoRule `json:"rules"`
}

func (p ReferralAutoRules) validate() error {
	if len(p.Rules) > 100 {
		return fmt.Errorf("%w：最多 100 条", ErrReferralAutoRulesInvalid)
	}
	for i, rule := range p.Rules {
		if rule.LowerCents < 0 || rule.LowerCents > maxReferralRuleCents ||
			(rule.UpperCents != nil && (*rule.UpperCents <= rule.LowerCents || *rule.UpperCents > maxReferralRuleCents)) ||
			rule.RateUnits < 0 || rule.RateUnits >= referralRateScale {
			return fmt.Errorf("%w：第 %d 条的金额区间或比例不合法", ErrReferralAutoRulesInvalid, i+1)
		}
	}
	return nil
}

func (p ReferralAutoRules) active() bool {
	if !p.Enabled {
		return false
	}
	for _, rule := range p.Rules {
		if rule.Enabled {
			return true
		}
	}
	return false
}

func (p ReferralAutoRules) rateFor(creditedMicros int64) int64 {
	if !p.Enabled || creditedMicros <= 0 {
		return 0
	}
	// Integer cent thresholds preserve comparisons even for legacy records
	// stored with fractional cents. The award still uses the exact micros.
	cents := creditedMicros / ticketRefundMicrosPerCent
	for _, rule := range p.Rules {
		if rule.Enabled && cents >= rule.LowerCents && (rule.UpperCents == nil || cents < *rule.UpperCents) {
			return rule.RateUnits // a matching 0% rule wins too
		}
	}
	return 0
}

func GetReferralAutoRulesWithDB(db *gorm.DB) (ReferralAutoRules, error) {
	p := ReferralAutoRules{Rules: []ReferralAutoRule{}}
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return p, nil
	}
	var setting entity.SystemSetting
	err := referralSession(db).Where(&entity.SystemSetting{Key: ReferralAutoRulesSettingKey}).Take(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(setting.Value), &p); err != nil {
		return ReferralAutoRules{}, ErrReferralAutoRulesInvalid
	}
	if err = p.validate(); err != nil {
		return ReferralAutoRules{}, err
	}
	if p.Rules == nil {
		p.Rules = []ReferralAutoRule{}
	}
	return p, nil
}

func SaveReferralAutoRulesWithDB(db *gorm.DB, p ReferralAutoRules) error {
	if err := p.validate(); err != nil {
		return err
	}
	if p.Rules == nil {
		p.Rules = []ReferralAutoRule{}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	row := entity.SystemSetting{Key: ReferralAutoRulesSettingKey, Value: string(body), Remark: "自动邀请返佣规则（按优先级）"}
	return referralSession(db).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error
}

// Round non-negative wallet micros half up after applying an exact ratio.
// Each caller bounds the numerator by the (positive) denominator.
func referralRatioMicros(micros, numerator, denominator int64) int64 {
	p := new(big.Int).Mul(big.NewInt(micros), big.NewInt(numerator))
	p.Add(p, big.NewInt(denominator/2))
	return p.Quo(p, big.NewInt(denominator)).Int64()
}
