package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newReviewRebateMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:review-rebate-migration-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 旧库（users 无 gift_balance、无评价返现四张表）必须被补齐，
// 且历史用户按 gift_balance=0 保留（不猜测历史赠金）。
func TestEnsureReviewRebateSchemaAddsTablesAndColumns(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)

	// 模拟加列之前的老 users 表（故意不带 gift_balance）。
	if err := db.Exec(`CREATE TABLE users (
		id integer PRIMARY KEY AUTOINCREMENT,
		username varchar(255),
		password varchar(255),
		role varchar(32) NOT NULL DEFAULT 'user',
		status varchar(32) NOT NULL DEFAULT 'enabled',
		balance decimal(18,6) NOT NULL DEFAULT 0,
		register_source varchar(32) NOT NULL DEFAULT 'legacy',
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (username, password, balance) VALUES ('legacy-user', '123456', 12.5)`).Error; err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		t.Fatal("expected users.gift_balance to be added")
	}
	for _, table := range []string{
		entity.TableNameReviewRebateProgram,
		entity.TableNameReviewRebateRule,
		entity.TableNameReviewRebateClaim,
		entity.TableNameReviewRebateAward,
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s", table)
		}
	}

	// 历史用户按「从未拿过赠金」保留：gift_balance=0，balance 原样不动。
	var legacy entity.User
	if err := db.Where("username = ?", "legacy-user").First(&legacy).Error; err != nil {
		t.Fatalf("load legacy user: %v", err)
	}
	if legacy.GiftBalance != 0 {
		t.Fatalf("legacy user gift_balance must be 0, got %v", legacy.GiftBalance)
	}
	if legacy.Balance != 12.5 {
		t.Fatalf("legacy user balance must be preserved, got %v", legacy.Balance)
	}

	// 只跑一次：标记写入后第二次直接返回。
	if !systemSettingFlagIsSet(db, reviewRebateSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 唯一索引必须生效：一单一条 award、同账号同评价只有一条 claim。
func TestReviewRebateSchemaUniqueIndexes(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("migrate review rebate: %v", err)
	}

	claim := entity.ReviewRebateClaim{
		ProgramID: 1, UserID: 1, ScreenshotSHA256: "aaa", CanonicalClaimID: "canonical-1",
		Status: entity.ReviewRebateStatusSubmitted,
	}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatalf("create claim: %v", err)
	}
	duplicateClaim := claim
	duplicateClaim.ID = 0
	if err := db.Create(&duplicateClaim).Error; err == nil {
		t.Fatal("duplicate (program_id,user_id,canonical_claim_id) must be rejected")
	}
	// 换活动是**新行**（次数按 user 跨活动统计，去重按 program 计）。
	otherProgram := claim
	otherProgram.ID = 0
	otherProgram.ProgramID = 2
	if err := db.Create(&otherProgram).Error; err != nil {
		t.Fatalf("same canonical id under another program must be allowed: %v", err)
	}

	award := entity.ReviewRebateAward{
		ClaimID: claim.ID, UserID: 1, Amount: 0.5, GiftSource: entity.ReviewRebateGiftSource,
		PolicyVersion: "v1",
	}
	if err := db.Create(&award).Error; err != nil {
		t.Fatalf("create award: %v", err)
	}
	duplicateAward := award
	duplicateAward.ID = 0
	if err := db.Create(&duplicateAward).Error; err == nil {
		t.Fatal("duplicate claim_id must be rejected by the unique index")
	}
}

// users 表还不存在时（极端空库）直接返回，不报错 —— 交给 AutoMigrate(User) 处理。
func TestEnsureReviewRebateSchemaSkipsWhenUsersMissing(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("expected no error when users is missing, got %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameReviewRebateClaim) {
		t.Fatal("must not create review rebate tables before users exists")
	}
	// 没建结构就不该写标记（下次启动要重跑）。
	if systemSettingFlagIsSet(db, reviewRebateSchemaReadyKey) {
		t.Fatal("must not write the migration flag when nothing was migrated")
	}
}

// 迁移必须是幂等的：结构已存在、标记丢失时重跑不报错也不改数据。
func TestEnsureReviewRebateSchemaIsIdempotent(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 故意清掉标记，模拟「标记写失败」的场景。
	if err := db.Where(map[string]any{"key": reviewRebateSchemaReadyKey}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("re-run without flag must succeed: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		t.Fatal("gift_balance must still exist after re-run")
	}
}
