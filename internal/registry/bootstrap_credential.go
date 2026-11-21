package registry

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func applyInitialAdminCredential(database *gorm.DB, plan adminBootstrapPlan) error {
	return database.Transaction(func(tx *gorm.DB) error {
		// Claim the PK *before* reading a missing row. Under InnoDB two
		// SELECT ... FOR UPDATE gap locks can coexist and then deadlock on INSERT.
		// This insert acquires the writer lock without mutating an existing credential.
		if !plan.exists {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).
				Create(initialAdministrator("")).Error; err != nil {
				return err
			}
		}
		var current struct{ Password string }
		err := tx.Table(entity.TableNameUser).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("password").Where("id = ?", 1).Take(&current).Error
		if err != nil {
			return err
		}
		if current.Password != "" {
			return nil // another initializer or operator won; don't touch the account
		}
		plan.exists = true
		return writeAdminBootstrap(tx, plan)
	})
}
