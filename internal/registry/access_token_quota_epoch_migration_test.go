package registry

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newQuotaEpochMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:quota-epoch-migration-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestEnsureAccessTokenQuotaEpochSchemaCreatesOverlayTable 老库补齐：
// 建出 access_token_quota_epochs + 唯一 (token_id, epoch)，且**不动 access_tokens**。
func TestEnsureAccessTokenQuotaEpochSchemaCreatesOverlayTable(t *testing.T) {
	db := newQuotaEpochMigrationTestDB(t)

	// 模拟重置功能上线前的库：只有 users / access_tokens / logs / system_settings。
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.Log{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	owner := entity.User{ID: 2, Username: "legacy-owner", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "admin", Balance: 8}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	token := entity.AccessToken{
		ID: 1, UserID: owner.ID, Name: "legacy-token",
		TokenHash:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		TokenPrefix: "tk_legacy", TokenValue: "tk_synthetic_legacy_quota_epoch",
		Status: "enabled", TotalQuotaLimit: 20, TotalAmountUsed: 12,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameAccessTokenQuotaEpoch) {
		t.Fatal("overlay table must not exist before the migration")
	}

	if err := ensureAccessTokenQuotaEpochSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasTable(entity.TableNameAccessTokenQuotaEpoch) {
		t.Fatal("expected access_token_quota_epochs table")
	}
	for _, column := range []string{"token_id", "epoch", "reset_at", "baseline_amount", "operator_id", "event_id"} {
		if !db.Migrator().HasColumn(entity.TableNameAccessTokenQuotaEpoch, column) {
			t.Fatalf("expected column %s on access_token_quota_epochs", column)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameAccessTokenQuotaEpoch, "uk_access_token_quota_epochs_token_epoch") {
		t.Fatal("expected unique index (token_id, epoch)")
	}

	// 红线：历史累计与限额一列都没动，也没有凭空造 epoch 行。
	var stored entity.AccessToken
	if err := db.First(&stored, token.ID).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if stored.TotalAmountUsed != 12 || stored.TotalQuotaLimit != 20 {
		t.Fatalf("migration must not touch access_tokens: used=%v limit=%v", stored.TotalAmountUsed, stored.TotalQuotaLimit)
	}
	var count int64
	if err := db.Model(&entity.AccessTokenQuotaEpoch{}).Count(&count).Error; err != nil {
		t.Fatalf("count epochs: %v", err)
	}
	if count != 0 {
		t.Fatalf("migration must not backfill epoch rows, got %d", count)
	}
}

// TestEnsureAccessTokenQuotaEpochSchemaIsIdempotentAndMarked 跑两次不报错，
// 且第二次被 system_settings 标记短路（表结构保持）。
func TestEnsureAccessTokenQuotaEpochSchemaIsIdempotentAndMarked(t *testing.T) {
	db := newQuotaEpochMigrationTestDB(t)

	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureAccessTokenQuotaEpochSchema(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !systemSettingFlagIsSet(db, accessTokenQuotaEpochSchemaReadyKey) {
		t.Fatal("expected migration marker to be written")
	}
	if err := ensureAccessTokenQuotaEpochSchema(db); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAccessTokenQuotaEpoch) {
		t.Fatal("table must survive the second run")
	}

	// 唯一索引真的生效：重复 (token_id, epoch) 必须被拒绝。
	row := entity.AccessTokenQuotaEpoch{TokenID: 1, Epoch: 1, ResetAt: time.Now(), BaselineAmount: 12, OperatorID: 1, EventID: "e1"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("first epoch insert: %v", err)
	}
	duplicate := entity.AccessTokenQuotaEpoch{TokenID: 1, Epoch: 1, ResetAt: time.Now(), BaselineAmount: 12, OperatorID: 1, EventID: "e2"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("unique index (token_id, epoch) must reject duplicates")
	}
	// 不同 epoch 可以共存。
	second := entity.AccessTokenQuotaEpoch{TokenID: 1, Epoch: 2, ResetAt: time.Now(), BaselineAmount: 17, OperatorID: 1, EventID: "e3"}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("second epoch must be insertable: %v", err)
	}
}

// TestEnsureAccessTokenQuotaEpochSchemaRepairsMissingUniqueIndex 唯一索引被人手工删掉时，
// 迁移要能补回来（幂等自检，而不是只靠 AutoMigrate 的 tag）。
func TestEnsureAccessTokenQuotaEpochSchemaRepairsMissingUniqueIndex(t *testing.T) {
	db := newQuotaEpochMigrationTestDB(t)

	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.AccessTokenQuotaEpoch{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const indexName = "uk_access_token_quota_epochs_token_epoch"
	if !db.Migrator().HasIndex(entity.TableNameAccessTokenQuotaEpoch, indexName) {
		t.Fatalf("AutoMigrate should create %s", indexName)
	}
	if err := db.Migrator().DropIndex(entity.TableNameAccessTokenQuotaEpoch, indexName); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if db.Migrator().HasIndex(entity.TableNameAccessTokenQuotaEpoch, indexName) {
		t.Fatal("index should be gone before repair")
	}

	if err := ensureAccessTokenQuotaEpochUniqueIndex(db); err != nil {
		t.Fatalf("repair unique index: %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameAccessTokenQuotaEpoch, indexName) {
		t.Fatalf("expected %s to be recreated", indexName)
	}
}
