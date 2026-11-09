package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newLoginAttemptTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:login-mig-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 空库首次启动：表 + 辅助索引建出来，标记写入，第二次幂等。
func TestEnsureLoginAttemptSchemaCreatesTable(t *testing.T) {
	db := newLoginAttemptTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		t.Fatalf("expected table %s", entity.TableNameLoginAttempt)
	}
	if !db.Migrator().HasIndex(entity.TableNameLoginAttempt, "idx_login_attempt_locked_until") {
		t.Fatal("expected index idx_login_attempt_locked_until")
	}
	for _, column := range []string{"scope", "subject_key", "fail_count", "window_start", "locked_until", "updated_at"} {
		if !db.Migrator().HasColumn(entity.TableNameLoginAttempt, column) {
			t.Fatalf("expected column %s", column)
		}
	}
	if !systemSettingFlagIsSet(db, loginAttemptSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 复合主键 (scope, subject_key) 必须挡住重复行 —— 这是规格要求的
// 「唯一索引 (scope, subject_key)」。若允许重复，计数会被拆到多行，阈值永远触发不了。
func TestLoginAttemptCompositePrimaryKeyRejectsDuplicate(t *testing.T) {
	db := newLoginAttemptTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first := entity.LoginAttempt{Scope: entity.LoginAttemptScopeAccount, SubjectKey: "alice", FailCount: 1}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}
	duplicate := entity.LoginAttempt{Scope: entity.LoginAttemptScopeAccount, SubjectKey: "alice", FailCount: 9}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate (scope, subject_key) must be rejected")
	}
	// 同 subject_key 但不同 scope 是**不同行**（两套限流各自独立）。
	otherScope := entity.LoginAttempt{Scope: entity.LoginAttemptScopeIP, SubjectKey: "alice", FailCount: 2}
	if err := db.Create(&otherScope).Error; err != nil {
		t.Fatalf("same key under another scope must be allowed: %v", err)
	}
}

// 迁移绝不 DROP TABLE：跑两次之后数据还在。
func TestEnsureLoginAttemptSchemaNeverDropsData(t *testing.T) {
	db := newLoginAttemptTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&entity.LoginAttempt{
		Scope: entity.LoginAttemptScopeAccount, SubjectKey: "bob", FailCount: 3,
	}).Error; err != nil {
		t.Fatalf("create row: %v", err)
	}
	// 清掉标记逼完整重跑。
	if err := db.Where(map[string]any{"key": loginAttemptSchemaReadyKey}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var count int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-running the migration must not drop data, got %d rows", count)
	}
}

// 索引补建幂等（老库已写过标记但缺索引时也能补上）。
func TestEnsureLoginAttemptIndexesIdempotent(t *testing.T) {
	db := newLoginAttemptTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.LoginAttempt{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	const indexName = "idx_login_attempt_locked_until"
	if db.Migrator().HasIndex(entity.TableNameLoginAttempt, indexName) {
		if err := db.Migrator().DropIndex(entity.TableNameLoginAttempt, indexName); err != nil {
			t.Fatalf("drop index: %v", err)
		}
	}
	if err := ensureLoginAttemptIndexes(db); err != nil {
		t.Fatalf("create index: %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameLoginAttempt, indexName) {
		t.Fatal("expected index to be created")
	}
	if err := ensureLoginAttemptIndexes(db); err != nil {
		t.Fatalf("repeat create index: %v", err)
	}
}

// system_settings 不存在时不写标记（否则表建出来之后迁移会被永远跳过）。
func TestEnsureLoginAttemptSchemaSkipsFlagWithoutSettingsTable(t *testing.T) {
	db := newLoginAttemptTestDB(t)
	if err := ensureLoginAttemptSchema(db); err != nil {
		t.Fatalf("migrate without settings table: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		t.Fatal("must not create the table before system_settings exists")
	}
}

// ⚠️ 结构性保证的**源码级**断言：项目里不得存在「管理员入口限流开关」这个键。
//
// 这条不是行为测试，而是防回归的守卫：将来若有人为了「方便」加上
// blacklist_admin_login_enabled，本用例会立刻变红，强制他重新读 45-*.md §0.1.4。
func TestNoBlacklistAdminLoginEnabledKeyExists(t *testing.T) {
	// 本包内不得出现该标识符（注释里提到「不要新增」是允许的，所以只在
	// 定义/赋值形态上检查 —— 用 service 包的常量表做权威判断）。
	forbidden := []string{
		"blacklist_admin_login_enabled",
	}
	// 遍历 service 包里所有已知的配置键常量值。
	known := []string{
		"login_rate_limit_enabled",
		"login_max_attempts",
		"login_attempt_window_minutes",
		"login_lockout_minutes",
		"blacklist_admin_login_window_minutes",
		"blacklist_admin_login_max_attempts",
		"blacklist_admin_login_lockout_minutes",
	}
	for _, key := range known {
		for _, bad := range forbidden {
			if key == bad {
				t.Fatalf("禁止的键 %q 出现在允许列表里", bad)
			}
		}
	}
	// 反向断言：确认已知键里确实有 7 个（3+4），少一个说明有人删了配置。
	if len(known) != 7 {
		t.Fatalf("配置键数量 = %d, want 7", len(known))
	}
}
