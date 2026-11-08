package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// Add the multiplier only. Existing fixed prices and explicit zero multipliers
// are financial data, not an invitation to rewrite an account's tariff at boot.
func ensureScriptPriceMultiplierSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameUserScript) {
		return nil
	}
	if !db.Migrator().HasColumn(&entity.UserScript{}, "AnswerPriceMultiplier") {
		if err := db.Migrator().AddColumn(&entity.UserScript{}, "AnswerPriceMultiplier"); err != nil {
			return fmt.Errorf("add script answer price multiplier: %w", err)
		}
	}
	return nil
}
