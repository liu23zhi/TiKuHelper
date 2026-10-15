package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// billingClientIPSchemaKey 标记「billing_records.client_ip 已就绪」。
	billingClientIPSchemaKey = "billing_client_ip_schema_ready"
	// billingClientIPBackfilledKey 标记「账单来源 IP 已按 requestId 尽力回填」。
	billingClientIPBackfilledKey = "billing_client_ip_backfilled"
)

// ensureBillingClientIPSchema 为账单表补上 client_ip 列，并尽力回填历史来源 IP。
//
// 历史行只在日志描述中存在完整 `requestId=<值>` 且能精确匹配时回填；
// 没有 requestId 或日志已清理的记录保持为空，不根据用户/IP/时间猜测关联关系。
// 建列失败返回错误；数据回填失败只记日志，不阻断启动，并允许下次启动重试。
func ensureBillingClientIPSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil
	}

	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "client_ip") {
		// SQLite 走原生 ALTER（与其它 ensure* 迁移同构）。
		if strings.EqualFold(db.Dialector.Name(), "sqlite") {
			if err := db.Exec("ALTER TABLE " + entity.TableNameBillingRecord + " ADD COLUMN client_ip varchar(64)").Error; err != nil {
				return err
			}
		} else if err := db.Migrator().AddColumn(&entity.BillingRecord{}, "ClientIP"); err != nil {
			return err
		}
		logger.SysLog("已为 billing_records 补上 client_ip 列")
	}

	if systemSettingFlagIsSet(db, billingClientIPBackfilledKey) {
		return nil
	}
	if err := safeBackfillBillingClientIP(db); err != nil {
		// 数据回填失败不能阻断启动，保留空值并允许下次启动重试。
		logger.SysLog(fmt.Sprintf("账单来源 IP 回溯失败（不影响启动，将保留在下次启动重试）：%v", err))
		return nil
	}
	if err := setSystemSettingFlag(db, billingClientIPBackfilledKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入账单来源 IP 回溯标记失败（下次启动会重新回溯）：%v", err))
	}
	return nil
}

// safeBackfillBillingClientIP 在回填外面兜一层 recover，避免历史脏数据影响启动。
func safeBackfillBillingClientIP(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillBillingClientIP(db)
}

type billingClientIPRow struct {
	ID        int32  `gorm:"column:id"`
	RequestID string `gorm:"column:request_id"`
	ClientIP  string `gorm:"column:client_ip"`
}

// backfillBillingClientIP 只依据日志中的完整 requestId 精确回填来源 IP。
func backfillBillingClientIP(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameLog) ||
		!db.Migrator().HasColumn(entity.TableNameBillingRecord, "request_id") {
		return nil
	}

	const batchSize = 500
	var lastID int32
	updated := 0
	for {
		rows := make([]billingClientIPRow, 0, batchSize)
		if err := db.Table(entity.TableNameBillingRecord).
			Select("id, request_id, client_ip").
			Where("id > ? AND COALESCE(client_ip, '') = '' AND COALESCE(request_id, '') <> ''", lastID).
			Order("id asc").
			Limit(batchSize).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			lastID = row.ID
			clientIP, err := findBillingClientIPInLogs(db, strings.TrimSpace(row.RequestID))
			if err != nil {
				return err
			}
			if clientIP == "" {
				continue
			}
			if err := db.Table(entity.TableNameBillingRecord).
				Where("id = ? AND COALESCE(client_ip, '') = ''", row.ID).
				Update("client_ip", clientIP).Error; err != nil {
				return err
			}
			updated++
		}
	}

	if updated > 0 {
		logger.SysLog(fmt.Sprintf("账单来源 IP 已从 requestId 审计日志回溯，共回填 %d 条", updated))
	}
	return nil
}

// findBillingClientIPInLogs 先筛出包含 requestId 标记的日志，再在 Go 中比较完整值，
// 避免短 requestId 被长 requestId 的前缀误命中。
func findBillingClientIPInLogs(db *gorm.DB, requestID string) (string, error) {
	if requestID == "" {
		return "", nil
	}
	var candidates []struct {
		Description string `gorm:"column:description"`
		ClientIP    string `gorm:"column:client_ip"`
	}
	if err := db.Table(entity.TableNameLog).
		Select("description, client_ip").
		Where("INSTR(description, ?) > 0 AND COALESCE(client_ip, '') <> ''", "requestId=").
		Order("id asc").
		Scan(&candidates).Error; err != nil {
		return "", err
	}

	for _, candidate := range candidates {
		if extractRequestID(candidate.Description) == requestID {
			return strings.TrimSpace(candidate.ClientIP), nil
		}
	}
	return "", nil
}

func extractRequestID(description string) string {
	const marker = "requestId="
	index := strings.Index(description, marker)
	if index < 0 {
		return ""
	}
	remainder := strings.TrimSpace(description[index+len(marker):])
	if remainder == "" {
		return ""
	}
	return strings.Fields(remainder)[0]
}
