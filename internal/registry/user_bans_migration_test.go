package registry

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 本文件钉死 user_bans 迁移的五条判据（任务书 ①–④）。
//
// # 为什么每条都必须存在（问「如果实现坏了，这条会不会红？」）
//
//	① 空库首启建表成功      写错成「先写标记再建表」→ 表永远不存在，且无报错
//	② 二次启动不重跑        写错成「不判断标记」→ 每次启动都 AutoMigrate（日志重复）
//	③ 标记后删表不重建      写错成「索引/建表放在标记判断之后」→ 本用例覆盖不到；
//	                        它真正覆盖的是「标记短路是否有效」：短路没了表就会重建
//	④ 变异：短路 setSystemSettingFlag → ③ 必红（变异脚本在汇报里，不改本文件）
//
// ② 与 ③ 是**两个不同的事**：② 断言「表在建时不会被重建」（标记前的短路），
// ③ 断言「表不在时也不会被重建」（标记是唯一权威，结构存在与否不参与判定）。
// 只有 ② 时，把实现改成「先 HasTable，有表就 return」也能过 —— 但那在表被误删后
// 会重建出空表，丢掉全部封禁审计。③ 专门抓这个。

func newUserBansMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:user-bans-migration-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// userBansLogCapture 把 logger.SysLog 的输出抓下来。
//
// logger.SysLog 最终写 gin.DefaultWriter（pkg/logger/logger.go 的 writeMessage），
// 所以换掉它即可，不必给 logger 加可注入的 hook。
func userBansLogCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldInfo, oldError := gin.DefaultWriter, gin.DefaultErrorWriter
	buffer := &bytes.Buffer{}
	gin.DefaultWriter = buffer
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldInfo, oldError })
	return buffer
}

// ① 空库首次启动：user_bans 表 + 两条索引都建出来，标记写进去。
func TestUserBansSchemaCreatesTableAndIndexesOnEmptyDB(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatalf("expected table %s to be created on an empty DB", entity.TableNameUserBan)
	}
	for _, index := range []string{"idx_user_ban_user_expires", "idx_user_ban_banned_at"} {
		if !db.Migrator().HasIndex(entity.TableNameUserBan, index) {
			t.Fatalf("expected index %s", index)
		}
	}
	// 列集必须与 121 §4.8 一致：缺任何一列，封禁记录就解释不了「谁/为什么/到什么时候」。
	for _, column := range []string{"user_id", "reason", "banned_at", "expires_at", "banned_by", "created_at", "updated_at"} {
		if !db.Migrator().HasColumn(entity.TableNameUserBan, column) {
			t.Fatalf("expected column %s on %s", column, entity.TableNameUserBan)
		}
	}
	if !systemSettingFlagIsSet(db, userBansSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
}

// ② 二次启动不重跑：日志里「已建表 user_bans」只能出现**一次**。
//
// 断言日志而不是只断言 HasTable：表在第二次运行时本来就在，
// 「表存在」对「有没有重跑」毫无区分度（这正是结构断言的失效点）。
func TestUserBansSchemaSecondRunDoesNotLogCreateAgain(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	logs := userBansLogCapture(t)
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := strings.Count(logs.String(), "已建表 "+entity.TableNameUserBan); got != 1 {
		t.Fatalf("first run must log the creation once, got %d in:\n%s", got, logs.String())
	}

	logs.Reset()
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := strings.Count(logs.String(), "已建表 "+entity.TableNameUserBan); got != 0 {
		t.Fatalf("second run must not re-create the table, got %d creation logs in:\n%s", got, logs.String())
	}
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("table disappeared after the second run")
	}
}

// ③ 标记写入后人为删表再启动 → **表不重建**。
//
// 这是「标记是唯一权威」的判据：结构存在与否不参与判定。
// 若实现改成「HasTable 判存在」，表被误删后会被重建成空表 ——
// 表现是「封禁记录全没了，但服务一切正常」。
func TestUserBansSchemaDoesNotRecreateDroppedTableAfterFlag(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !systemSettingFlagIsSet(db, userBansSchemaReadyKey) {
		t.Fatal("precondition: the migration flag must be written before dropping the table")
	}
	if err := db.Migrator().DropTable(entity.TableNameUserBan); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("precondition: the table must be gone")
	}

	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("run after drop: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("the flag is authoritative: a dropped table must NOT be recreated")
	}
}

// 老库已经跑过标记（表在）但索引缺失：索引补建必须发生在标记判断**之前**。
func TestUserBansSchemaBackfillsIndexesOnAlreadyMarkedDB(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.UserBan{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	// 模拟「历史版本的 AutoMigrate 建了表但没建自定义索引」的库。
	for _, index := range []string{"idx_user_ban_user_expires", "idx_user_ban_banned_at"} {
		if db.Migrator().HasIndex(entity.TableNameUserBan, index) {
			if err := db.Migrator().DropIndex(entity.TableNameUserBan, index); err != nil {
				t.Fatalf("drop %s: %v", index, err)
			}
		}
	}
	// 标记已经写过 —— 若索引补建放在标记判断之后，这里就永远补不上。
	if err := setSystemSettingFlag(db, userBansSchemaReadyKey); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, index := range []string{"idx_user_ban_user_expires", "idx_user_ban_banned_at"} {
		if !db.Migrator().HasIndex(entity.TableNameUserBan, index) {
			t.Fatalf("expected %s to be backfilled on an already-marked DB", index)
		}
	}
}

// 迁移绝不 DROP TABLE：清掉标记逼完整重跑后，既有封禁记录必须一条不少。
func TestUserBansSchemaNeverDropsExistingRows(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.UserBan{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row := entity.UserBan{UserID: 7, Reason: "保留测试", BannedBy: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create ban row: %v", err)
	}

	// 强行清掉标记，逼迁移完整重跑一遍（相当于「标记丢失」）。
	if err := db.Where(map[string]any{"key": userBansSchemaReadyKey}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}

	var count int64
	if err := db.Model(&entity.UserBan{}).Count(&count).Error; err != nil {
		t.Fatalf("count bans: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-running the migration must not drop ban rows: count=%d", count)
	}
}

// 极端空库（连 system_settings 都没有）：什么都不建，**也绝不写标记**。
//
// 反过来（先建表或先写标记）会让 system_settings 建出来后本迁移被永远跳过 ——
// 表永远不存在，判定永久降级为只读 users.status，且只有一条 WARN。
func TestUserBansSchemaSkipsEntirelyWhenSystemSettingsMissing(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate on bare DB: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("must not create user_bans when system_settings does not exist (nothing could record the flag)")
	}

	// system_settings 建出来后必须还能补建（证明上一步没有留下「已完成」的证据）。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("migrate after system_settings: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("expected user_bans to be created once system_settings exists")
	}
}

// 两套方言的 DDL 必须分别正确：SQLite 带 IF NOT EXISTS，MySQL 不带（它不支持）。
func TestUserBansIndexDDLBothDialects(t *testing.T) {
	spec := userBanIndexSpec{
		name:    "idx_user_ban_user_expires",
		table:   entity.TableNameUserBan,
		columns: []string{"user_id", "expires_at"},
	}
	sqliteDDL := userBanIndexDDL("sqlite", spec)
	// 用字面量断言：期望值**不能**用 entity.TableNameUserBan 拼 ——
	// 那样表名常量漂移时期望值会同步漂移，断言恒真（自我指涉）。
	wantSQLite := "CREATE INDEX IF NOT EXISTS `idx_user_ban_user_expires` ON `user_bans` (`user_id`, `expires_at`)"
	if sqliteDDL != wantSQLite {
		t.Fatalf("sqlite DDL = %q, want %q", sqliteDDL, wantSQLite)
	}
	mysqlDDL := userBanIndexDDL("mysql", spec)
	wantMySQL := "CREATE INDEX `idx_user_ban_user_expires` ON `user_bans` (`user_id`, `expires_at`)"
	if mysqlDDL != wantMySQL {
		t.Fatalf("mysql DDL = %q, want %q", mysqlDDL, wantMySQL)
	}
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatal("MySQL DDL must not use IF NOT EXISTS (MySQL rejects it with 1064)")
	}
	// 未知方言返回空串，由调用方报错，而不是瞎猜一句 SQL 出去。
	if got := userBanIndexDDL("postgres", spec); got != "" {
		t.Fatalf("unknown dialect DDL = %q, want empty", got)
	}
}

// nil db 直接返回 nil（导出函数可能被测试/工具以 nil 调用）。
func TestUserBansSchemaNilDBReturnsNil(t *testing.T) {
	var db *gorm.DB
	if err := EnsureUserBansSchema(db); err != nil {
		t.Fatalf("nil db must return nil, got %v", err)
	}
}

// 并发启动两个实例：两个 goroutine 同时跑迁移，都不能报错，且标记最终在。
//
// 真实风险是「同时判定索引不存在，一个撞 already exists」——
// ensureUserBanIndexSpecs 的错误分支会再核一次元数据，本用例把这个接缝跑一遍。
func TestUserBansSchemaConcurrentStartupIsIdempotent(t *testing.T) {
	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			errs[slot] = EnsureUserBansSchema(db)
		}(i)
	}
	wg.Wait()
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("concurrent run %d failed: %v", slot, err)
		}
	}
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		t.Fatal("expected user_bans after concurrent startup")
	}
	if !systemSettingFlagIsSet(db, userBansSchemaReadyKey) {
		t.Fatal("expected the flag after concurrent startup")
	}
}
