package registry

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// billingClientIPSchemaKey 标记「billing_records.client_ip 已就绪」。
	billingClientIPSchemaKey = "billing_client_ip_schema_ready"
)

// ensureBillingClientIPSchema 为账单表补上 client_ip 列。
//
// 用途：账单明细页展示来源 IP 归属地（形如 `1.2.3.4 （广东深圳）`）。
//
// **历史行无法回溯**：logs 表没有 request_id，账单与日志之间没有任何可关联的键，
// 因此加列之前落账的账单只能留空，前端按「只显示 -」降级。这不是遗漏，
// 而是数据本身就是缺失的 —— 所以这里只建列，不做假回填。
//
// 与既有 ensure* 迁移一致：建列失败返回错误（结构性故障要让人看见），
// 失败不写标记以便下次启动重试。
func ensureBillingClientIPSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil
	}
	if db.Migrator().HasColumn(entity.TableNameBillingRecord, "client_ip") {
		return nil
	}

	// SQLite 走原生 ALTER（与其它 ensure*Schema 同构）。
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameBillingRecord + " ADD COLUMN client_ip varchar(64)").Error; err != nil {
			return err
		}
	} else if err := db.Migrator().AddColumn(&entity.BillingRecord{}, "ClientIP"); err != nil {
		return err
	}

	logger.SysLog("已为 billing_records 补上 client_ip 列（历史账单该列为空，无法回溯，属数据本身缺失）")
	return nil
}
