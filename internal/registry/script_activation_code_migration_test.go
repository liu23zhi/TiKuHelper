package registry

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAdminScriptActivationMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(filepath.Join(t.TempDir(), "admin-script-activation.db"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// 迁移会写 system_settings 标记，前置表必须先建。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// 建表 + 唯一索引必须都到位。
//
// code 上的唯一索引是「一次性」的**物理保障**（并发兑换靠唯一约束 +
// 条件更新决出唯一赢家），所以它不是可选优化：缺了它，同一码在并发下
// 有可能被兑换两次。这条用例专门断言索引存在。
func TestEnsureAdminScriptActivationCodeSchemaCreatesTableAndUniqueIndex(t *testing.T) {
	db := newAdminScriptActivationMigrationDB(t)

	if db.Migrator().HasTable(entity.TableNameAdminScriptActivationCode) {
		t.Fatal("前置检查：建表前不应存在该表")
	}
	if err := EnsureAdminScriptActivationCodeSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAdminScriptActivationCode) {
		t.Fatal("建表失败")
	}
	if !db.Migrator().HasIndex(entity.TableNameAdminScriptActivationCode, "idx_admin_script_activation_code") {
		t.Fatal("code 唯一索引缺失 —— 一次性语义失去物理保障")
	}

	// 唯一约束要真的生效：重复 code 必须插入失败。
	first := entity.AdminScriptActivationCode{Code: "TKA-duplicate", Status: entity.ScriptActivationCodeStatusUnused}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("首次插入: %v", err)
	}
	second := entity.AdminScriptActivationCode{Code: "TKA-duplicate", Status: entity.ScriptActivationCodeStatusUnused}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("重复 code 必须被唯一索引拒绝")
	} else if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("拒绝原因应是唯一约束，实际 %v", err)
	}
}

// 索引补建必须真的建**唯一**索引 —— 不能只建普通索引。
//
// 这条用例存在的原因（实测踩到）：上一条用例在「AutoMigrate 已经按 gorm tag
// 建好唯一索引」的路径上，把 ensureAdminScriptActivationCodeIndexes 里的
// `unique = "UNIQUE "` 改成空串**不会变红** —— 因为表是 AutoMigrate 建的，
// 索引早就有了，补建分支直接 `HasIndex → continue`。
// 也就是说上一条只覆盖了「建表那一次」，覆盖不到「老表缺唯一索引时补建」。
//
// 这里显式模拟那个真实场景：先建一张**没有**唯一索引的表，再驱动补建，
// 然后断言「重复 code 会被拒绝」。
func TestEnsureAdminScriptActivationCodeIndexesUpgradesToUnique(t *testing.T) {
	db := newAdminScriptActivationMigrationDB(t)

	// 手工建表，且**不建**任何索引（模拟「老库有表但缺唯一索引」）。
	if err := db.Exec(`CREATE TABLE ` + entity.TableNameAdminScriptActivationCode + ` (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		code varchar(64) NOT NULL DEFAULT '',
		status varchar(16) NOT NULL DEFAULT 'unused',
		remark varchar(255) NOT NULL DEFAULT '',
		created_by integer NOT NULL DEFAULT 0,
		expires_at datetime,
		used_at datetime,
		used_by integer,
		used_script_id integer,
		used_entitlement_id integer,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("建老表: %v", err)
	}
	if db.Migrator().HasIndex(entity.TableNameAdminScriptActivationCode, "idx_admin_script_activation_code") {
		t.Fatal("前置检查：老表本不应有唯一索引")
	}

	if err := ensureAdminScriptActivationCodeIndexes(db); err != nil {
		t.Fatalf("补建索引: %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameAdminScriptActivationCode, "idx_admin_script_activation_code") {
		t.Fatal("补建后仍缺索引")
	}
	// 真正判据：补建的必须是 UNIQUE，不是普通索引。
	if err := db.Create(&entity.AdminScriptActivationCode{Code: "TKA-dup"}).Error; err != nil {
		t.Fatalf("首次插入: %v", err)
	}
	if err := db.Create(&entity.AdminScriptActivationCode{Code: "TKA-dup"}).Error; err == nil {
		t.Fatal("补建出来的索引必须是 UNIQUE —— 否则同一码可被插入两次")
	}
}

// 幂等：跑两次不报错，也不重复建索引。
func TestEnsureAdminScriptActivationCodeSchemaIsIdempotent(t *testing.T) {
	db := newAdminScriptActivationMigrationDB(t)
	for round := 0; round < 2; round++ {
		if err := EnsureAdminScriptActivationCodeSchema(db); err != nil {
			t.Fatalf("第 %d 轮 ensure schema: %v", round+1, err)
		}
	}
	if !db.Migrator().HasTable(entity.TableNameAdminScriptActivationCode) {
		t.Fatal("幂等运行后表应存在")
	}
	if !systemSettingFlagIsSet(db, adminScriptActivationSchemaReadyKey) {
		t.Fatal("成功后应写入一次性标记")
	}
}
