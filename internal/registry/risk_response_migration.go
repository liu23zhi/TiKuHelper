package registry

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// Separate from the old warnings schema marker: existing installations need the
// response ledger upgrade and the per-rule snapshot, not only new installations.
func ensureRiskResponseSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&entity.RiskWarning{}) ||
		!db.Migrator().HasTable(&entity.RiskRule{}) ||
		!db.Migrator().HasTable(&entity.SystemSetting{}) {
		return nil
	}
	if systemSettingFlagIsSet(db, service.RiskRuleResponseMigrationKey) {
		return nil
	}
	if !db.Migrator().HasColumn(&entity.RiskRule{}, "response_policy_json") {
		if err := db.Migrator().AddColumn(&entity.RiskRule{}, "ResponsePolicyJSON"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&entity.RiskWarningResponse{}); err != nil {
		return err
	}
	// Fill missing built-ins (never tombstones) before freezing legacy policy:
	// even a seeded row that was previously absent participated via the fallback.
	if err := seedDefaultRiskRules(db); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := service.MigrateLegacyRiskRulePolicies(tx); err != nil {
			return err
		}
		return setSystemSettingFlag(tx, service.RiskRuleResponseMigrationKey)
	})
}
