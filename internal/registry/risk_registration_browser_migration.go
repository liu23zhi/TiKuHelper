package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const registrationBrowserSchemaReadyKey = "risk_registration_browser_events_v1"

// Independent of the v2 rule flag already present on deployed databases. Don't
// backfill from historical IPs/user agents: there was no browser evidence then.
func ensureRegistrationBrowserSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) || systemSettingFlagIsSet(db, registrationBrowserSchemaReadyKey) {
		return nil
	}
	if err := db.AutoMigrate(&entity.RegistrationBrowserEvent{}); err != nil {
		return fmt.Errorf("migrate registration browser events: %w", err)
	}
	seedDefaultRiskRules(db) // inserts only missing rule IDs, including on v2; never resets operator settings/tombstones.
	return setSystemSettingFlag(db, registrationBrowserSchemaReadyKey)
}
