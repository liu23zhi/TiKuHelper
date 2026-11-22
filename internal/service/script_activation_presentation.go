package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// Read once for the whole list. Consumption blocks future starts without
// disabling the script or revoking the already active client's session.
func attachScriptRunConsumption(db *gorm.DB, items []ScriptListItem) error {
	ids := make([]int32, 0)
	for _, item := range items {
		if item.BillingMode == entity.ScriptBillingModeActivation {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var consumed []entity.ScriptActivationEntitlement
	if err := db.Session(&gorm.Session{NewDB: true}).Select("script_id", "run_consumed_at").Where("script_id IN ? AND run_consumed_at IS NOT NULL", ids).Find(&consumed).Error; err != nil {
		return err
	}
	byScript := make(map[int32]entity.ScriptActivationEntitlement, len(consumed))
	for _, entry := range consumed {
		if entry.ScriptID != nil {
			byScript[*entry.ScriptID] = entry
		}
	}
	for i := range items {
		entry, ok := byScript[items[i].ID]
		if !ok {
			continue
		}
		items[i].RunConsumedAt = entry.RunConsumedAt
		items[i].Command, items[i].Instruction, items[i].CompatibilityCommand, items[i].DownloadURL = "", "", "", ""
		for _, grant := range []*ScriptShortAuthorizationView{items[i].ShortAuthorization, items[i].LauncherAuthorization} {
			if grant != nil {
				grant.Status, grant.Command = "consumed", ""
			}
		}
	}
	return nil
}
