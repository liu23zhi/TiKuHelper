package registry

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 事件表相关的字面量与 internal/service 里的订单状态常量保持一致；
// registry 包不依赖 service 包，所以这里直接用字面量。
const (
	paymentEventStatusPaidFixture     = "paid"
	paymentEventStatusRefundedFixture = "refunded"
)

func newPaymentEventMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:payment-event-migration-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 老库（有 users / recharge_orders，但没有事件表）必须被补齐，并写迁移标记。
func TestEnsurePaymentEventSchemaCreatesTableWithUniqueOrderNo(t *testing.T) {
	db := newPaymentEventMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNamePaymentEvent) {
		t.Fatal("precondition: payment_events must not exist yet")
	}

	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("ensure payment event schema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNamePaymentEvent) {
		t.Fatal("expected payment_events to be created")
	}
	if !systemSettingFlagIsSet(db, paymentEventSchemaReadyKey) {
		t.Fatal("expected the one-shot migration marker to be written")
	}

	// 一笔网关订单一条事件：order_no 必须有唯一索引（幂等靠它）。
	var uniqueIndexes int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql LIKE '%order_no%' AND sql LIKE '%UNIQUE%'`,
		entity.TableNamePaymentEvent).Scan(&uniqueIndexes).Error; err != nil {
		t.Fatalf("inspect unique index: %v", err)
	}
	if uniqueIndexes == 0 {
		t.Fatal("expected a UNIQUE index covering order_no")
	}

	// 事件不可变：表里不应存在 updated_at 列（只增不改）。
	if db.Migrator().HasColumn(entity.TableNamePaymentEvent, "updated_at") {
		t.Fatal("payment_events must be append-only: no updated_at column")
	}
}

// 回填必须以 paid_at 为判据（而不是 status='paid'），覆盖「已退款」的历史订单。
func TestEnsurePaymentEventSchemaBackfillsPaidAtOrdersIncludingRefunded(t *testing.T) {
	db := newPaymentEventMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	paidAt := time.Now().Add(-time.Hour)
	orders := []entity.RechargeOrder{
		{UserID: 1, OrderNo: "rcg_pe_paid", PaymentProvider: "epay", Amount: 12.35,
			Status: paymentEventStatusPaidFixture, PaidAt: &paidAt, ThirdPartyOrderNo: "trade_pe_paid"},
		{UserID: 2, OrderNo: "rcg_pe_refunded", PaymentProvider: "epay", Amount: 28,
			Status: paymentEventStatusRefundedFixture, PaidAt: &paidAt, ThirdPartyOrderNo: "trade_pe_refunded"},
		// 从未入账：没有 paid_at，不该产生事件。
		{UserID: 3, OrderNo: "rcg_pe_pending", PaymentProvider: "epay", Amount: 8, Status: "pending"},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatalf("seed recharge orders: %v", err)
	}

	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("ensure payment event schema: %v", err)
	}

	var events []entity.PaymentEvent
	if err := db.Order("id").Find(&events).Error; err != nil {
		t.Fatalf("load payment events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 backfilled events (paid + refunded), got %d: %+v", len(events), events)
	}
	byOrderNo := make(map[string]entity.PaymentEvent, len(events))
	for _, event := range events {
		byOrderNo[event.OrderNo] = event
	}
	refunded, ok := byOrderNo["rcg_pe_refunded"]
	if !ok {
		t.Fatalf("refunded order must still get an event (paid_at proves it was credited): %+v", events)
	}
	if refunded.Amount != 28 || refunded.TradeNo != "trade_pe_refunded" || refunded.UserID != 2 ||
		refunded.Source != paymentEventBackfillSource {
		t.Fatalf("unexpected backfilled refunded event: %+v", refunded)
	}
	if _, ok := byOrderNo["rcg_pe_pending"]; ok {
		t.Fatal("order without paid_at must not get an event")
	}
	if _, ok := byOrderNo["rcg_pe_paid"]; !ok {
		t.Fatal("paid order must get an event")
	}
}

// 迁移必须幂等：标记已在时再来一次不报错、不重建、不丢数据、不重复回填。
func TestEnsurePaymentEventSchemaIsIdempotent(t *testing.T) {
	db := newPaymentEventMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	paidAt := time.Now().Add(-time.Hour)
	if err := db.Create(&entity.RechargeOrder{UserID: 1, OrderNo: "rcg_pe_idem", PaymentProvider: "epay",
		Amount: 88, Status: paymentEventStatusPaidFixture, PaidAt: &paidAt}).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	var before entity.PaymentEvent
	if err := db.Where("order_no = ?", "rcg_pe_idem").Take(&before).Error; err != nil {
		t.Fatalf("load backfilled event: %v", err)
	}

	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("second ensure must be a no-op, got: %v", err)
	}
	var count int64
	if err := db.Model(&entity.PaymentEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if count != 1 {
		t.Fatalf("idempotent rerun must not duplicate events, got %d", count)
	}
	var after entity.PaymentEvent
	if err := db.Where("order_no = ?", "rcg_pe_idem").Take(&after).Error; err != nil {
		t.Fatalf("reload event: %v", err)
	}
	if after.ID != before.ID || !after.CreatedAt.Equal(before.CreatedAt) || after.Amount != before.Amount {
		t.Fatalf("existing event mutated: before=%+v after=%+v", before, after)
	}
}

// recharge_orders 还不存在（极端空库首次启动）时不报错，且**不写标记** ——
// 否则订单表建出来之后本迁移会被永久跳过。
func TestEnsurePaymentEventSchemaSkipsWhenOrderTableMissing(t *testing.T) {
	db := newPaymentEventMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("ensure with missing recharge_orders must not fail: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNamePaymentEvent) {
		t.Fatal("must not create the event table before recharge_orders exists")
	}
	if systemSettingFlagIsSet(db, paymentEventSchemaReadyKey) {
		t.Fatal("must not write the marker when nothing was applied")
	}
}

// 回填失败只记日志、不阻断启动、且不写标记（下次启动自动重试）。
func TestEnsurePaymentEventSchemaKeepsMarkerOffWhenBackfillFails(t *testing.T) {
	db := newPaymentEventMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.RechargeOrder{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	paidAt := time.Now().Add(-time.Hour)
	if err := db.Create(&entity.RechargeOrder{UserID: 1, OrderNo: "rcg_pe_backfill_fail", PaymentProvider: "epay",
		Amount: 12, Status: paymentEventStatusPaidFixture, PaidAt: &paidAt}).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	// 让回填阶段的批量 Create 失败：只拦 payment_events，建表阶段不受影响。
	const callbackName = "test:payment_event_backfill_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNamePaymentEvent {
			tx.AddError(errors.New("synthetic backfill failure"))
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("backfill failure must not block startup: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNamePaymentEvent) {
		t.Fatal("structural creation must still succeed when backfill fails")
	}
	if systemSettingFlagIsSet(db, paymentEventSchemaReadyKey) {
		t.Fatal("marker must stay off so the next startup retries the backfill")
	}

	// 去掉故障后重跑：回填成功并写标记。
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatalf("remove callback: %v", err)
	}
	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("retry ensure: %v", err)
	}
	if !systemSettingFlagIsSet(db, paymentEventSchemaReadyKey) {
		t.Fatal("retry must write the marker after a successful backfill")
	}
	var count int64
	if err := db.Model(&entity.PaymentEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("retry must backfill exactly one event: count=%d err=%v", count, err)
	}
}

// nil 库直接返回，不 panic（启动路径上的调用方守卫）。
func TestEnsurePaymentEventSchemaNilDB(t *testing.T) {
	if err := ensurePaymentEventSchema(nil); err != nil {
		t.Fatalf("nil db must be a no-op, got: %v", err)
	}
}
