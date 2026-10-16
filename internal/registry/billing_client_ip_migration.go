package registry

import (
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// billingClientIPSchemaKey 标记「billing_records.client_ip 已就绪」。
	billingClientIPSchemaKey = "billing_client_ip_schema_ready"
	// billingClientIPBackfilledKey 标记「账单来源 IP 已按 requestId 尽力回填」。
	billingClientIPBackfilledKey = "billing_client_ip_backfilled"
	// billingClientIPBackfillBatchSize 是回填时账单表的分批大小。
	billingClientIPBackfillBatchSize = 500
	// requestIDLogMarker 是日志描述里 requestId 的固定前缀。
	requestIDLogMarker = "requestId="
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
//
// 分三遍：
//  1. 只读账单表，收集「待回填的 requestId」集合（日志表一次都不碰）；
//  2. 日志表**全表只扫一次**，按 id asc 建立 requestId→client_ip 索引，早出现的日志优先；
//  3. 再分批把命中结果写回账单表。
//
// ⚠️ 日志扫描次数必须与待回填账单条数无关。旧实现是「每条待回填账单都在 Go 里全表
// 扫一遍 logs」，实测 3 万行日志约 190ms/次（线上即为 500 条账单 ≈ 18s 起步），
// 账单上千条就会把启动拖到几分钟。改这里时必须保留「日志只扫一次」这一约束。
func backfillBillingClientIP(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameLog) ||
		!db.Migrator().HasColumn(entity.TableNameBillingRecord, "request_id") {
		return nil
	}

	wanted, err := collectPendingBillingRequestIDs(db)
	if err != nil {
		return err
	}
	if len(wanted) == 0 {
		return nil
	}

	index, err := indexLogClientIPByRequestID(db, wanted)
	if err != nil {
		return err
	}
	if len(index) == 0 {
		return nil
	}

	startedAt := time.Now()
	updated, err := applyBillingClientIPBackfill(db, index)
	if err != nil {
		return err
	}
	if updated > 0 {
		logger.SysLog(fmt.Sprintf("账单来源 IP 已从 requestId 审计日志回溯，共回填 %d 条（耗时 %dms）",
			updated, time.Since(startedAt).Milliseconds()))
	}
	return nil
}

// collectPendingBillingRequestIDs 分批读出「有 request_id 但还没有 client_ip」的账单，
// 只保留去重后的 requestId 集合；日志表在收集阶段完全不需要读取。
func collectPendingBillingRequestIDs(db *gorm.DB) (map[string]struct{}, error) {
	wanted := make(map[string]struct{})
	var lastID int32
	for {
		rows := make([]billingClientIPRow, 0, billingClientIPBackfillBatchSize)
		if err := db.Table(entity.TableNameBillingRecord).
			Select("id, request_id").
			Where("id > ? AND COALESCE(client_ip, '') = '' AND COALESCE(request_id, '') <> ''", lastID).
			Order("id asc").
			Limit(billingClientIPBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return wanted, nil
		}
		for _, row := range rows {
			lastID = row.ID
			if requestID := strings.TrimSpace(row.RequestID); requestID != "" {
				wanted[requestID] = struct{}{}
			}
		}
	}
}

// indexLogClientIPByRequestID 把日志表完整扫一遍（仅此一次），建立 requestId→client_ip 索引。
//
// 用 Rows() 流式读取，只为 wanted 里的 requestId 保留结果：
// 内存占用与「待回填账单条数」同量级，而不是与「日志条数」同量级。
func indexLogClientIPByRequestID(db *gorm.DB, wanted map[string]struct{}) (map[string]string, error) {
	rows, err := db.Table(entity.TableNameLog).
		Select("description, client_ip").
		Where("INSTR(description, ?) > 0 AND COALESCE(client_ip, '') <> ''", requestIDLogMarker).
		Order("id asc").
		Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	index := make(map[string]string, len(wanted))
	for rows.Next() {
		var (
			description string
			clientIP    string
		)
		if err := rows.Scan(&description, &clientIP); err != nil {
			return nil, err
		}
		requestID := extractRequestID(description)
		if requestID == "" {
			continue
		}
		if _, ok := wanted[requestID]; !ok {
			continue
		}
		// 同一 requestId 有多条日志时保留最早一条，与旧的 Order("id asc") 首次命中一致。
		if _, exists := index[requestID]; exists {
			continue
		}
		if ip := strings.TrimSpace(clientIP); ip != "" {
			index[requestID] = ip
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return index, nil
}

// applyBillingClientIPBackfill 分批把索引里的来源 IP 写回账单表。
// 一批一个事务，避免为每条 UPDATE 付一次提交开销。
func applyBillingClientIPBackfill(db *gorm.DB, index map[string]string) (int, error) {
	updated := 0
	var lastID int32
	for {
		rows := make([]billingClientIPRow, 0, billingClientIPBackfillBatchSize)
		if err := db.Table(entity.TableNameBillingRecord).
			Select("id, request_id, client_ip").
			Where("id > ? AND COALESCE(client_ip, '') = '' AND COALESCE(request_id, '') <> ''", lastID).
			Order("id asc").
			Limit(billingClientIPBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return updated, err
		}
		if len(rows) == 0 {
			return updated, nil
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			for _, row := range rows {
				lastID = row.ID
				clientIP, ok := index[strings.TrimSpace(row.RequestID)]
				if !ok {
					continue
				}
				if err := tx.Table(entity.TableNameBillingRecord).
					Where("id = ? AND COALESCE(client_ip, '') = ''", row.ID).
					Update("client_ip", clientIP).Error; err != nil {
					return err
				}
				updated++
			}
			return nil
		}); err != nil {
			return updated, err
		}
	}
}

func extractRequestID(description string) string {
	index := strings.Index(description, requestIDLogMarker)
	if index < 0 {
		return ""
	}
	remainder := strings.TrimSpace(description[index+len(requestIDLogMarker):])
	if remainder == "" {
		return ""
	}
	return strings.Fields(remainder)[0]
}
