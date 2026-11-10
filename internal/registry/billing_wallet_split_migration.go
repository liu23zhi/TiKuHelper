package registry

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureBillingWalletSplitSchema 为账单表补上 gift_charged / principal_charged 两列。
//
// 为什么需要它们：双桶模型下消费先扣赠金、再扣本金，而「可退金额」只能由
// **已消耗的本金部分**决定（赠金消耗不影响可退额）。没有分桶记录就只能靠猜，
// 任何猜测都会写错钱包口径。
//
// 只建列、**不回填**：历史行没有分桶信息（旧模型 balance 是总可用，
// 账单里也没有「本次从哪个桶扣」的字段），无法可靠还原。
// 读侧必须把 0 值当「未知」处理，而不是「没扣本金」。
//
// 与其它 ensure* 迁移一致：建列失败返回错误（结构性故障要让人看见）；
// HasColumn 幂等，每次启动核一次元数据即可，不写一次性标记。
func ensureBillingWalletSplitSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil
	}
	columns := []struct {
		name  string
		field string
		ddl   string
	}{
		{"gift_charged", "GiftCharged", "decimal(18,8) NOT NULL DEFAULT 0"},
		{"principal_charged", "PrincipalCharged", "decimal(18,8) NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if db.Migrator().HasColumn(entity.TableNameBillingRecord, column.name) {
			continue
		}
		if strings.EqualFold(db.Dialector.Name(), "sqlite") {
			// SQLite 走原生 ALTER（与 search_mode / gift_balance 同因：
			// 驱动的 AddColumn 支持不如 MySQL 稳定）。
			if err := db.Exec("ALTER TABLE " + entity.TableNameBillingRecord +
				" ADD COLUMN " + column.name + " " + column.ddl).Error; err != nil {
				return err
			}
			continue
		}
		if err := db.Migrator().AddColumn(&entity.BillingRecord{}, column.field); err != nil {
			return err
		}
	}
	return nil
}
