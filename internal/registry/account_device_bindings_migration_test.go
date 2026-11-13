package registry

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 设备池迁移测试（121 §4.9 / 107 Q15-Q17）。
//
// 核心不是「表建出来了」（最低门槛），而是四条纪律的语义：
//   - FLAG 写入后二次启动**不重跑**；
//   - 建表失败时**绝不写标记**（否则表永远建不出来且没有任何报错）；
//   - 索引补建**放在标记判断之前**（老库否则永远补不到新索引）；
//   - **无历史回填**（本仓最容易被下一个人「顺手补上」的一条）。
//
// 外加一条行为级断言：唯一索引 (user_id, device_id) 真的挡住重复插入。
// 那条索引是「同一设备不占新槽」的唯一保障，只断言它「存在」不够 ——
// 索引存在但列顺序反了（device_id, user_id）同样能建出来，语义却不变，
// 所以这里直接插两次验行为。

// accountDeviceBindingTestDBSeq 保证每个用例拿到独立的内存库。
//
// `file:xxx?mode=memory&cache=shared` 在同一进程内同名库是**共享**的；
// 表驱动/重名用例不加序号会互相污染（本仓已踩过）。
var accountDeviceBindingTestDBSeq atomic.Int64

// newAccountDeviceBindingTestDB 建一个只含 system_settings 的空库。
//
// 刻意**不预建 account_device_bindings**：本组用例要验证「空库首启能建出来」。
func newAccountDeviceBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:device-binding-migration-" + name + "-" +
		itoaAtomic(accountDeviceBindingTestDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	return db
}

// 空库首启：表 + 唯一索引都建出来，标记写进去。
func TestEnsureAccountDeviceBindingsSchemaCreatesTableAndIndex(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)

	if db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("account_device_bindings 不该在迁移前存在（否则本用例测不到建表）")
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatalf("expected table %s", entity.TableNameAccountDeviceBinding)
	}
	// 任务书指定的五列 + 时间戳，少一列 = 功能静默缺字段。
	for _, column := range []string{
		"id", "user_id", "device_id", "first_seen_at", "last_seen_at", "created_at", "updated_at",
	} {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("expected column %s.%s", entity.TableNameAccountDeviceBinding, column)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameAccountDeviceBinding, "uk_account_device_binding_user_device") {
		t.Fatal("expected index uk_account_device_binding_user_device")
	}
	if !systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		t.Fatal("expected the migration flag to be written")
	}
}

// 标记生效：二次启动**不重跑**（删表后不该重建）。
func TestEnsureAccountDeviceBindingsSchemaSecondRunSkipsWhenFlagSet(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		t.Fatal("标记未写入，后续断言无意义")
	}
	if err := db.Migrator().DropTable(entity.TableNameAccountDeviceBinding); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("标记已写 + 表被外部删掉时，第二次运行**不该**重建表" +
			"——出现这张表说明 FLAG 判断被绕过（例如短路成 if false）")
	}
}

// 建表失败时**绝不写标记**（121 §2.1 的反直觉细节）。
//
// 造「建表失败」的手法与公告迁移一致：同库放一个同名视图。
// SQLite 的 CREATE TABLE 会因「already an object named ...」失败，
// 而 HasTable 对视图返回 false（它只查 table 类型），因此迁移会真的走到建表分支。
//
// 若这条用例写错（例如用 DropTable 之后测），就会得到「标记在但表没有」的
// 静默 100% 失效状态：设备池永远建不出来，且没有任何报错。
func TestEnsureAccountDeviceBindingsSchemaDoesNotWriteFlagWhenCreateFails(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)

	if err := db.Exec("CREATE VIEW " + entity.TableNameAccountDeviceBinding + " AS SELECT 1 AS id").Error; err != nil {
		t.Fatalf("create blocking view: %v", err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err == nil {
		t.Fatal("建表被同名对象挡住时必须返回 error（结构性失败要让人看见）")
	}
	if systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		t.Fatal("建表失败时**绝不写标记**：否则这张表将永远建不出来且无任何报错")
	}

	// 清掉障碍后重跑必须成功 —— 证明上一步失败没有留下持久副作用。
	if err := db.Exec("DROP VIEW " + entity.TableNameAccountDeviceBinding).Error; err != nil {
		t.Fatalf("drop blocking view: %v", err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("rerun after clearing blocker: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("障碍清除后应当建表成功")
	}
	if !systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		t.Fatal("障碍清除后应当写入标记")
	}
}

// 索引补建在标记判断**之前**（121 §2.2）。
//
// 场景：老库已经跑过标记（表建好），但后来新增/调整的索引等不到 AutoMigrate。
// 若索引只在 FLAG 之后建，这类库永远补不到索引 —— 表现只是「查询慢」，不报错。
func TestEnsureAccountDeviceBindingsSchemaBackfillsIndexAfterFlagSet(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !systemSettingFlagIsSet(db, accountDeviceBindingsSchemaV1Key) {
		t.Fatal("标记未写入，后续断言无意义")
	}
	name := "uk_account_device_binding_user_device"
	if err := db.Migrator().DropIndex(entity.TableNameAccountDeviceBinding, name); err != nil {
		t.Fatalf("drop index %s: %v", name, err)
	}
	if db.Migrator().HasIndex(entity.TableNameAccountDeviceBinding, name) {
		t.Fatalf("索引 %s 未真的删掉，用例前置不成立", name)
	}

	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameAccountDeviceBinding, name) {
		t.Fatalf("标记已写时仍必须补建索引 %s（索引补建放在标记判断之前）", name)
	}
}

// 唯一索引**行为级**断言：同一 (user_id, device_id) 插第二次必须失败。
//
// 只断言「索引存在」不够：索引存在但列顺序写反（device_id, user_id）也照样存在，
// 语义不变但完全测不出问题。这里直接插两次看库层是否拒绝。
func TestAccountDeviceBindingsUniqueIndexRejectsDuplicate(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first := entity.AccountDeviceBinding{UserID: 7, DeviceID: "device-a"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// 不同用户用同一台设备必须允许（设备是公共的，绑定按账号隔离）。
	if err := db.Create(&entity.AccountDeviceBinding{UserID: 8, DeviceID: "device-a"}).Error; err != nil {
		t.Fatalf("同一设备不同用户应当允许：%v", err)
	}
	// 同一用户同一设备必须被唯一索引挡住。
	if err := db.Create(&entity.AccountDeviceBinding{UserID: 7, DeviceID: "device-a"}).Error; err == nil {
		t.Fatal("同一 (user_id, device_id) 重复插入必须被唯一索引拒绝（幂等锚点）")
	}
}

// 两方言的索引 DDL 必须各自合法（MySQL 不支持 IF NOT EXISTS）。
func TestAccountDeviceBindingIndexDDLPerDialect(t *testing.T) {
	spec := accountDeviceBindingIndexSpec{
		name:    "uk_account_device_binding_user_device",
		table:   entity.TableNameAccountDeviceBinding,
		columns: []string{"user_id", "device_id"},
		unique:  true,
	}
	sqliteDDL := accountDeviceBindingIndexDDL("SQLite", spec)
	if !strings.Contains(sqliteDDL, "UNIQUE INDEX") {
		t.Fatalf("唯一索引必须出 UNIQUE INDEX：%s", sqliteDDL)
	}
	if !strings.Contains(sqliteDDL, "IF NOT EXISTS") {
		t.Fatalf("SQLite DDL 必须带 IF NOT EXISTS（DDL 层幂等）：%s", sqliteDDL)
	}
	if !strings.Contains(sqliteDDL, "`user_id`, `device_id`") {
		t.Fatalf("列顺序必须是 (user_id, device_id)：%s", sqliteDDL)
	}
	mysqlDDL := accountDeviceBindingIndexDDL("mysql", spec)
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatalf("MySQL 的 CREATE INDEX 没有 IF NOT EXISTS（会报 1064）：%s", mysqlDDL)
	}
	if mysqlDDL == "" {
		t.Fatal("mysql DDL 不该为空")
	}
	if unknown := accountDeviceBindingIndexDDL("postgres", spec); unknown != "" {
		t.Fatalf("未知方言必须返回空串让调用方报错，实际 %q", unknown)
	}
}

// nil 库必须直接返回 nil 而不是 panic（本仓 28/29 个迁移都这么写）。
func TestEnsureAccountDeviceBindingsSchemaNilDB(t *testing.T) {
	if err := ensureAccountDeviceBindingsSchema(nil); err != nil {
		t.Fatalf("nil db 应当返回 nil：%v", err)
	}
	if err := ensureAccountDeviceBindingIndexes(nil); err != nil {
		t.Fatalf("nil db 建索引应当返回 nil：%v", err)
	}
}

// 迁移是幂等的：连续三次运行都不报错。
func TestEnsureAccountDeviceBindingsSchemaIsIdempotent(t *testing.T) {
	db := newAccountDeviceBindingTestDB(t)
	for i := 0; i < 3; i++ {
		if err := ensureAccountDeviceBindingsSchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("三次运行后表必须存在")
	}
}

// 判据 ⑤（源码级）：迁移里**不允许出现任何 UPDATE users 语句**。
//
// 这不是形式主义。「无历史回填」是 121 §4.9 的硬前提：上线前的历史请求
// 根本没有设备指纹，任何凭空造出来的行都等于「替用户决定他已占用 N 台设备」，
// 会把存量用户直接锁在上限之外，而且这种误判事后无法自证。
//
// 断言方式是读源文件而不是查数据库：数据库断言只能证明「这次没回填」，
// 而这条约束要钉住的是「代码里就写不出回填」。
//
// ⚠️ 必须**先剥掉 Go 注释行**再匹配：本文件的注释里刻意写明了
// 「不允许出现任何 UPDATE users」，若连注释一起匹配，这条用例会把自己的
// 说明文字当成违规（假红）。剥注释后匹配的才是真的语句/字符串字面量。
func TestAccountDeviceBindingsMigrationHasNoUserBackfill(t *testing.T) {
	raw, err := os.ReadFile("account_device_bindings_migration.go")
	if err != nil {
		t.Fatalf("read migration source: %v", err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	lower := strings.ToLower(code.String())
	forbidden := []string{
		"update users",
		"update `users`",
		"update \"users\"",
		"insert into users",
		"insert into `users`",
	}
	for _, statement := range forbidden {
		if strings.Contains(lower, statement) {
			t.Fatalf("设备池迁移里不允许出现 %q —— 121 §4.9 明确「无历史回填」："+
				"历史请求没有设备指纹，伪造历史行会把存量用户直接锁在上限之外", statement)
		}
	}
	// 正向断言：这条约束真的落在了「没有回填」而不是「恰好没写 UPDATE」。
	// 迁移只该做建表 + 建索引，因此被迁移的实体只该是 AccountDeviceBinding。
	if !strings.Contains(lower, "automigrate(&entity.accountdevicebinding{})") {
		t.Fatal("迁移必须 AutoMigrate(&entity.AccountDeviceBinding{})")
	}
	// entity 名里带 User 的迁移语句一律不许出现在本文件（users 是回填的唯一入口）。
	if strings.Contains(lower, "entity.user{}") {
		t.Fatal("设备池迁移不该 AutoMigrate entity.User{}（那会连带建 users 表并暗示回填）")
	}
}
