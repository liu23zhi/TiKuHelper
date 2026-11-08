package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// The quota preservation path deliberately skips the normal AccessToken
// AutoMigrate. Only add the new column/index needed by the script release;
// never reconstruct the table or rewrite the historical quota to do so.
func ensureScriptKeyLinkOnPreservedQuotaSchema(db *gorm.DB) error {
	m := db.Migrator()
	key := &entity.AccessToken{}
	if !m.HasColumn(key, "ScriptID") {
		if err := m.AddColumn(key, "ScriptID"); err != nil {
			return fmt.Errorf("add script_id: %w", err)
		}
	}
	const index = "idx_access_tokens_script_id"
	if !m.HasIndex(key, index) {
		if err := m.CreateIndex(key, index); err != nil {
			return fmt.Errorf("create script_id index: %w", err)
		}
	}
	return nil
}
