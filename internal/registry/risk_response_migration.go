package registry

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// Separate from the old warnings schema marker: installations that already have warnings need the ledger too.
func ensureRiskResponseSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&entity.RiskWarning{}) || db.Migrator().HasTable(&entity.RiskWarningResponse{}) {
		return nil
	}
	return db.AutoMigrate(&entity.RiskWarningResponse{})
}
