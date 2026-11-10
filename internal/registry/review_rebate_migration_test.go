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
	// 机制列 deleted_at 由 helper 带上（否则 GORM 软删除条件会报 no such column）。
	createLegacyUsersTable(t, db)
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

// agent_scorer 是「真实打分器」落地时新增的列：已经跑过一次性标记的老库
// 不会再走 AutoMigrate，必须由 ensureReviewRebateClaimScorerColumn 单独补上。
//
// 回归重点：不能因为标记已写就跳过补列 —— 那会让老库上的 claim 表永远缺这一列，
// 写入时直接 SQL 报错（而不是静默丢字段）。
func TestEnsureReviewRebateSchemaAddsScorerColumnToExistingTable(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	// 模拟老库：把新增列删掉，并保留一次性标记（标记已写 = 不会再 AutoMigrate）。
	if db.Migrator().HasColumn(entity.TableNameReviewRebateClaim, "agent_scorer") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameReviewRebateClaim + " DROP COLUMN agent_scorer").Error; err != nil {
			t.Skipf("this sqlite build cannot drop columns: %v", err)
		}
	}
	if !systemSettingFlagIsSet(db, reviewRebateSchemaReadyKey) {
		t.Fatal("precondition: the one-shot flag must already be written")
	}

	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("re-run must add the new column even when the flag is set: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameReviewRebateClaim, "agent_scorer") {
		t.Fatal("expected review_rebate_claims.agent_scorer to be added")
	}
	// 历史行按空字符串保留（不推测是谁打的）。
	if err := db.Create(&entity.ReviewRebateClaim{
		ProgramID: 1, UserID: 1, ScreenshotSHA256: "abc", CanonicalClaimID: "c-scorer",
		Status: entity.ReviewRebateStatusSubmitted,
	}).Error; err != nil {
		t.Fatalf("create claim: %v", err)
	}
	var stored entity.ReviewRebateClaim
	if err := db.Where("canonical_claim_id = ?", "c-scorer").First(&stored).Error; err != nil {
		t.Fatalf("load claim: %v", err)
	}
	if stored.AgentScorer != "" {
		t.Fatalf("historical claim must keep an empty scorer marker, got %q", stored.AgentScorer)
	}
}

// gift_balance 下限是 0（用户明确要求「赠送金额只能为 0，不能为负数」）。
// 应用层每条扣减路径都 clamp，但漏一条就会在库里留下负值 ——
// 启动自检必须把它纠正掉，且**不能因为纠正失败就阻断启动**。
func TestEnsureReviewRebateSchemaClampsNegativeGiftBalance(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	// 造两条脏数据：负赠金与正常值。
	if err := db.Create(&entity.User{ID: 1, Username: "neg", Balance: 5, GiftBalance: -3}).Error; err != nil {
		t.Fatalf("create negative gift user: %v", err)
	}
	if err := db.Create(&entity.User{ID: 2, Username: "ok", Balance: 2, GiftBalance: 4}).Error; err != nil {
		t.Fatalf("create normal user: %v", err)
	}

	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("migration must not fail on dirty wallet data: %v", err)
	}

	var fixed entity.User
	if err := db.Where("id = ?", 1).First(&fixed).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fixed.GiftBalance != 0 {
		t.Fatalf("negative gift_balance must be clamped to 0, got %v", fixed.GiftBalance)
	}
	// 本金不受影响（它是欠款载体，允许为负）。
	if fixed.Balance != 5 {
		t.Fatalf("clamping the gift bucket must not touch the principal, got %v", fixed.Balance)
	}
	var untouched entity.User
	if err := db.Where("id = ?", 2).First(&untouched).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if untouched.GiftBalance != 4 {
		t.Fatalf("positive gift_balance must be left alone, got %v", untouched.GiftBalance)
	}
}

// 自检每次启动都跑（不受一次性标记约束）：标记已写时新增的负值也要被纠正。
func TestClampNegativeGiftBalanceRunsDespiteOneShotFlag(t *testing.T) {
	db := newReviewRebateMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}
	if !systemSettingFlagIsSet(db, reviewRebateSchemaReadyKey) {
		t.Fatal("precondition: the one-shot flag must be written")
	}
	// 标记已写之后再写入脏数据。
	if err := db.Create(&entity.User{ID: 3, Username: "late-neg", Balance: 1, GiftBalance: -1}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := ensureReviewRebateSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var stored entity.User
	if err := db.Where("id = ?", 3).First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.GiftBalance != 0 {
		t.Fatalf("self-check must run even when the flag is set, got %v", stored.GiftBalance)
	}
}
