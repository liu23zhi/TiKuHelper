package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newRechargePromotionMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:recharge-promotion-migration-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 老库（有 users 但既没有赠金账本表）必须被补齐，并写迁移标记。
func TestEnsureRechargePromotionSchemaAddsLedgerTable(t *testing.T) {
	db := newRechargePromotionMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameRechargePromotionGrant) {
		t.Fatal("precondition: grant table must not exist yet")
	}

	if err := ensureRechargePromotionSchema(db); err != nil {
		t.Fatalf("ensure recharge promotion schema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameRechargePromotionGrant) {
		t.Fatal("expected recharge_promotion_grants to be created")
	}
	if !systemSettingFlagIsSet(db, rechargePromotionSchemaReadyKey) {
		t.Fatal("expected the one-shot migration marker to be written")
	}

	// 一单一条：recharge_order_id 必须有唯一索引（幂等靠它，AGENTS.md §26.4）。
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql LIKE '%recharge_order_id%' AND sql LIKE '%UNIQUE%'`,
		entity.TableNameRechargePromotionGrant).Scan(&count).Error; err != nil {
		t.Fatalf("inspect unique index: %v", err)
	}
	if count == 0 {
		t.Fatal("expected a UNIQUE index covering recharge_order_id")
	}
}

// 迁移必须幂等：标记已在时再来一次不报错、不重建、不丢数据。
func TestEnsureRechargePromotionSchemaIsIdempotent(t *testing.T) {
	db := newRechargePromotionMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base schema: %v", err)
	}
	if err := ensureRechargePromotionSchema(db); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	row := entity.RechargePromotionGrant{
		RechargeOrderID: 11, OrderNo: "rcg_migration_keep", UserID: 1,
		PaidAmount: 88, Threshold: 88, Rate: 0.5, GiftAmount: 44,
		PolicyVersion: "v1", Remark: "充值阶梯赠送",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed grant row: %v", err)
	}

	if err := ensureRechargePromotionSchema(db); err != nil {
		t.Fatalf("second ensure must be a no-op, got: %v", err)
	}
	var stored entity.RechargePromotionGrant
	if err := db.Where("recharge_order_id = ?", 11).Take(&stored).Error; err != nil {
		t.Fatalf("idempotent rerun must keep existing rows: %v", err)
	}
	if stored.GiftAmount != 44 {
		t.Fatalf("existing row mutated: %+v", stored)
	}
}

// users 表还不存在（极端空库首次启动）时不报错，且**不写标记** ——
// 否则 users 建出来之后本迁移会被永久跳过。
func TestEnsureRechargePromotionSchemaSkipsWhenUsersMissing(t *testing.T) {
	db := newRechargePromotionMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	if err := ensureRechargePromotionSchema(db); err != nil {
		t.Fatalf("ensure with missing users must not fail: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameRechargePromotionGrant) {
		t.Fatal("must not create the ledger before users exists")
	}
	if systemSettingFlagIsSet(db, rechargePromotionSchemaReadyKey) {
		t.Fatal("must not write the marker when nothing was applied")
	}
}

// nil 库直接返回，不 panic（启动路径上的调用方守卫）。
func TestEnsureRechargePromotionSchemaNilDB(t *testing.T) {
	if err := ensureRechargePromotionSchema(nil); err != nil {
		t.Fatalf("nil db must be a no-op, got: %v", err)
	}
}
