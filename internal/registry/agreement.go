package registry

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func EnsureAgreementSchema(db *gorm.DB) error {
	if err := db.AutoMigrate(&entity.AgreementHead{}, &entity.AgreementDraft{}, &entity.AgreementRelease{}, &entity.AgreementDevice{}, &entity.AgreementConsent{}); err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.AgreementHead{ID: 1}).Error
}
