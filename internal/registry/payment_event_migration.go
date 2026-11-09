package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// paymentEventSchemaReadyKey 标记「支付已入账事件表已就绪（含历史订单回填）」。
	paymentEventSchemaReadyKey = "payment_event_schema_ready"
	// paymentEventBackfillBatchSize 是回填时 recharge_orders 的分批大小。
	paymentEventBackfillBatchSize = 500
	// paymentEventBackfillSource 标记回填产生的事件来源，与实时回调区分。
	paymentEventBackfillSource = "backfill"
)

// ensurePaymentEventSchema 建立「支付已入账」唯一事件表，并回填历史已入账订单。
//
// 覆盖：
//   - payment_events（order_no 唯一 → 一笔网关订单一条事件）
//
// 为什么必须有这张表：加款幂等原本只看 recharge_orders.status 是否 paid，
// 而 status 会被运营改成 refunded。网关重投同一条成功回调时「现在不等于 paid」，
// 于是同一个网关订单被加款第二次 —— 这是真实资金漏洞，见
// internal/entity/payment_event.go 的注释。
//
// 回填的判据是 `paid_at IS NOT NULL`（不是 status='paid'）：
// paid_at 只在加款路径写入，所以它才是「曾经入过账」的准确集合，
// 同时覆盖「现在仍是 paid」与「paid 之后被改成 refunded」两种情况。
// 不回填会让历史订单继续暴露在同一个漏洞下。
//
// 纪律（AGENTS.md 第 11 / 17 / 18b 条）：
//   - 结构性建表失败 → 返回错误，让启动 FATAL 看见；
//   - 数据回填失败 → 只记日志、不阻断启动，且**不写标记**，下次启动自动重试；
//   - 用 system_settings 标记保证只跑一次，失败不写标记；
//   - 不做任何 DROP TABLE / DROP COLUMN。
//
// 复杂度：recharge_orders **只按 id 升序扫一次**（每批 500 行），
// 内存与批大小同量级，不随订单总数增长；禁止在按行循环里再查大表。
func ensurePaymentEventSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, paymentEventSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsurePaymentEventSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 什么都没建（极端空库首次启动，recharge_orders 都还没有）：绝不能写标记，
		// 否则订单表建出来之后本迁移会被永久跳过。
		return nil
	}
	if err := safeBackfillPaymentEvents(db); err != nil {
		// 回填失败不能阻断启动：结构已就绪，实时回调已经受事件表保护；
		// 历史订单会在下次启动重试回填。标记不写 → 下次启动会重跑（幂等）。
		logger.SysLog(fmt.Sprintf("支付已入账事件回填失败（不影响启动，将在下次启动重试）：%v", err))
		return nil
	}
	if err := setSystemSettingFlag(db, paymentEventSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入支付事件迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsurePaymentEventSchema 在外层兜一层 recover。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsurePaymentEventSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyPaymentEventSchema(db)
}

func applyPaymentEventSchema(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameRechargeOrder) {
		// 充值订单表还没建出来：交给 AutoMigrate(RechargeOrder) 处理，
		// 下次启动订单表就存在了。
		return false, nil
	}
	if err := db.AutoMigrate(&entity.PaymentEvent{}); err != nil {
		return false, fmt.Errorf("create %s fail: %w", entity.TableNamePaymentEvent, err)
	}
	return true, nil
}

// safeBackfillPaymentEvents 在外层兜一层 recover，脏数据不能让服务反复崩溃。
func safeBackfillPaymentEvents(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return backfillPaymentEvents(db)
}

// paymentEventBackfillRow 是回填时从 recharge_orders 读出的最小列集合。
type paymentEventBackfillRow struct {
	ID      int32   `gorm:"column:id"`
	UserID  int32   `gorm:"column:user_id"`
	OrderNo string  `gorm:"column:order_no"`
	Amount  float64 `gorm:"column:amount"`
	TradeNo string  `gorm:"column:third_party_order_no"`
}

// backfillPaymentEvents 为历史已入账订单补齐事件行。
//
// 单次扫描：按 id 升序游标分批读，一批一个 Create（OnConflict DoNothing），
// 因此对 recharge_orders 的读取次数 = ceil(N/500)，与事件表已有行数无关。
func backfillPaymentEvents(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameRechargeOrder) ||
		!db.Migrator().HasTable(entity.TableNamePaymentEvent) {
		return nil
	}
	created := 0
	var lastID int32
	for {
		rows := make([]paymentEventBackfillRow, 0, paymentEventBackfillBatchSize)
		if err := db.Table(entity.TableNameRechargeOrder).
			Select("id, user_id, order_no, amount, third_party_order_no").
			// paid_at 只在加款路径写入 —— 它才是「曾经入过账」的准确判据，
			// 且不随 status 被改成 refunded 而丢失。
			Where("id > ? AND paid_at IS NOT NULL AND COALESCE(order_no, '') <> ''", lastID).
			Order("id asc").
			Limit(paymentEventBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		events := make([]entity.PaymentEvent, 0, len(rows))
		for _, row := range rows {
			lastID = row.ID
			events = append(events, entity.PaymentEvent{
				OrderNo:         row.OrderNo,
				RechargeOrderID: row.ID,
				UserID:          row.UserID,
				Amount:          row.Amount,
				TradeNo:         row.TradeNo,
				TradeStatus:     "",
				Source:          paymentEventBackfillSource,
			})
		}
		result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&events)
		if result.Error != nil {
			return result.Error
		}
		created += int(result.RowsAffected)
	}
	if created > 0 {
		logger.SysLog(fmt.Sprintf("已为历史充值订单补齐支付已入账事件，共 %d 条", created))
	}
	return nil
}
