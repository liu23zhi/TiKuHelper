package registry

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureBillingSearchModeSchema 为账单表补上 search_mode 列（深度思考 / 普通搜索）。
//
// 只建列、不回填：历史行无法可靠区分模式 —— `logs` 表没有 request_id，
// 账单与审计日志之间没有可关联键（同 client_ip 的回填方案），
// 因此历史行保持为空，由界面降级显示「-」。
//
// 与其它 ensure* 迁移一致：建列失败返回错误（结构性故障要让人看见）。
func ensureBillingSearchModeSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil
	}
	if db.Migrator().HasColumn(entity.TableNameBillingRecord, "search_mode") {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameBillingRecord + " ADD COLUMN search_mode varchar(16)").Error; err != nil {
			return err
		}
	} else if err := db.Migrator().AddColumn(&entity.BillingRecord{}, "SearchMode"); err != nil {
		return err
	}
	return nil
}
