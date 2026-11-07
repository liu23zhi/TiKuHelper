package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const scriptPriceMultiplierBackfilledKey = "script_price_multiplier_backfilled"

// ensureScriptPriceMultiplierSchema normalizes legacy script pricing to the
// multiplier model. The first run also clears the old fixed-price field so
// historical scripts are exactly 1X of the current tiku price. New rows are
// created after this marker and keep their explicit multiplier, including 0X.
func ensureScriptPriceMultiplierSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameUserScript) {
		return nil
	}
	if systemSettingFlagIsSet(db, scriptPriceMultiplierBackfilledKey) {
		return nil
	}
	if !db.Migrator().HasColumn(entity.TableNameUserScript, "answer_price_multiplier") {
		if err := db.Migrator().AddColumn(&entity.UserScript{}, "AnswerPriceMultiplier"); err != nil {
			return fmt.Errorf("add script answer price multiplier: %w", err)
		}
	}
	// A column introduced without a default may be NULL or zero on legacy
	// rows; normalize those rows before clearing the obsolete fixed price.
	if err := db.Table(entity.TableNameUserScript).
		Where("answer_price_multiplier IS NULL OR answer_price_multiplier = ?", 0).
		Update("answer_price_multiplier", 1).Error; err != nil {
		return fmt.Errorf("backfill script answer price multiplier: %w", err)
	}
	if err := db.Table(entity.TableNameUserScript).
		Where("answer_price_multiplier = ? AND answer_price <> ?", 1, 0).
		Update("answer_price", 0).Error; err != nil {
		return fmt.Errorf("clear legacy script answer price: %w", err)
	}
	if err := setSystemSettingFlag(db, scriptPriceMultiplierBackfilledKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入脚本答题倍率迁移标记失败（下次启动重试）：%v", err))
	}
	return nil
}
