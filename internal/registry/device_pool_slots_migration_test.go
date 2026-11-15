package registry

// 设备 ID 体系第二批（141 §3.1 / §3.2）的迁移验收：两列 + 唯一索引不变。
//
// 与既有 device_pool_binding_migration_test.go 分开成文件而不是追加：
// 那个文件守的是 A1 的 5 列与拉黑表，本文件守 141 的两列与「索引**不得**扩列」。
// 两批的验收判据方向相反（一个是「必须加上」，一个是「必须别加」），
// 混在一个文件里最容易被下一个人合并成一条。
//
// ## C1-4 为什么必须单独一条（而且必须是**行为级**）
//
// 141 §3.2 的约束是「uk_account_device_binding_user_device 仍为 (user_id, device_id)」。
// 只断言「索引存在」是无效判据 —— 把 client_instance 追加成第三列，索引照样「存在」。
// 必须同时钉两件事：
//  1. 元数据里的列清单**逐元素**等于 [user_id, device_id]（顺序也算）；
//  2. 行为上「同 user + 同 device + 不同 client_instance」仍然被唯一索引拒绝
//     —— 这一条才是「exe 三个会话不会变成三行」的真正保证。

import (
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 141 §3.1 要求的两列。
//
// 期望值写字面量而不是引用任何常量：本批次这两列没有对应常量，
// 将来若加了常量，这里的字面量仍能在常量漂移时变红。
// deviceSlotsColumnNames 是 141 第二批要求本表具备的列。
//
// session_reported_at 是后补的第 3 列（141 §5.4 的心跳时间戳）：
// 它在此之前的**唯一**问题就是「代码用 HasColumn 判它、有完整降级路径、有测试，
// 但没有任何迁移建它」—— 于是线上永远走「退回 last_seen_at」分支，
// S9（心跳超时）的正确性全靠「上报必刷 last_seen_at」这条隐式约定。
// 加进本清单 = 「空库首启必须建出来」的最直接判据。
var deviceSlotsColumnNames = []string{"client_instance", "session_count", "session_reported_at"}

// C1-1：空库首启时两列必须被建出来。
func TestDeviceSlotsSchemaCreatesSecondBatchColumns(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}
	for _, column := range deviceSlotsColumnNames {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("account_device_bindings 缺列 %s（141 §3.1 / C1-1）", column)
		}
	}
}

// C1-1 的**默认值**断言：必须走 DDL 默认值，而不是依赖应用层补写。
//
// 为什么必须钉 DDL 默认值：`session_count` 的 0 与 `client_instance` 的 ”
// 是 max(1, session_count) 口径的起点。若默认值是 NULL 或 1：
//
//	NULL ⇒ 扫描进 int32 报错 ⇒ CountDevices 失败 ⇒ CanUseDevice fail-open
//	        ⇒ 设备上限整体静默失效（且不报错）
//	1    ⇒ 普通浏览器凭空占 1 个额外槽位 ⇒ 上限 3 的用户只能绑 2 台
//
// 裸 INSERT（不写这两列）是唯一能证明「DDL 默认值生效」的写法：
// 用 GORM 的 Create 会把结构体零值显式写进 INSERT，测的就不是默认值了。
func TestDeviceSlotsColumnsDDLDefaults(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}
	if err := db.Exec("INSERT INTO "+entity.TableNameAccountDeviceBinding+
		" (user_id, device_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?)",
		77, "raw-default-dev", "2026-11-15 00:00:00", "2026-11-15 00:00:00").Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	var row struct {
		ClientInstance string `gorm:"column:client_instance"`
		SessionCount   int32  `gorm:"column:session_count"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("client_instance AS client_instance, session_count AS session_count").
		Where("device_id = ?", "raw-default-dev").Take(&row).Error; err != nil {
		t.Fatalf("read row: %v", err)
	}
	if row.ClientInstance != "" {
		t.Fatalf("client_instance 的列默认值必须是空串，实际 %q", row.ClientInstance)
	}
	if row.SessionCount != 0 {
		t.Fatalf("session_count 的列默认值必须是 0（max(1,0)=1 是 R4/R5/R7 的共同解），实际 %d",
			row.SessionCount)
	}
}

// C1-1 的**老库升级路径**：v1 标记已置位的库也必须补上这两列。
//
// 与 A1 的同类用例同一个理由：加列若写在标记判断之后，老库永远补不上，
// 而且不报错 —— 表现是「代码读 session_count ⇒ no such column ⇒ 计数 fail-open
// ⇒ 设备上限静默失效」。本用例把「老结构表 + 已置位标记」精确复现出来。
func TestDeviceSlotsSchemaBackfillsColumnsAfterLegacyFlagSet(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS " + entity.TableNameAccountDeviceBinding).Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	// 老结构：A1 的 5 列都有，但 141 的两列没有 —— 这正是「上一批已上线」的形态。
	legacyDDL := "CREATE TABLE " + entity.TableNameAccountDeviceBinding + " (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"user_id INTEGER NOT NULL, " +
		"device_id VARCHAR(191) NOT NULL, " +
		"first_seen_at DATETIME, last_seen_at DATETIME, created_at DATETIME, updated_at DATETIME, " +
		"owner_kind VARCHAR(16) NOT NULL DEFAULT 'login', " +
		"token_id INTEGER NOT NULL DEFAULT 0, " +
		"device_label VARCHAR(64) NOT NULL DEFAULT '', " +
		"revoked_at DATETIME NULL, " +
		"revoked_reason VARCHAR(32) NOT NULL DEFAULT '')"
	if err := db.Exec(legacyDDL).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX " + entity.AccountDeviceBindingUniqueIndex +
		" ON " + entity.TableNameAccountDeviceBinding + " (user_id, device_id)").Error; err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	if err := setSystemSettingFlag(db, accountDeviceBindingsSchemaV1Key); err != nil {
		t.Fatalf("set legacy flag: %v", err)
	}
	for _, column := range deviceSlotsColumnNames {
		if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("前置不成立：老库不该有列 %s", column)
		}
	}

	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("upgrade run: %v", err)
	}
	for _, column := range deviceSlotsColumnNames {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("老库（v1 标记已置位）必须补上列 %s —— 加列若写在标记判断之后，"+
				"这些库的新列永远补不上且不报错（表现为计数 fail-open、上限静默失效）", column)
		}
	}
}

// C1-1 幂等：连跑三次不报错，两列都在，且已有的 session_count 值不被重置。
func TestDeviceSlotsSchemaIsIdempotent(t *testing.T) {
	db := newA1RegistryDB(t)
	for i := 0; i < 3; i++ {
		if err := ensureAccountDeviceBindingsSchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if err := db.Exec("INSERT INTO "+entity.TableNameAccountDeviceBinding+
		" (user_id, device_id, first_seen_at, last_seen_at, session_count, client_instance) VALUES (?, ?, ?, ?, ?, ?)",
		88, "idem-dev", "2026-11-15 00:00:00", "2026-11-15 00:00:00", 3, "inst-a").Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("fourth run: %v", err)
	}
	var row struct {
		SessionCount   int32  `gorm:"column:session_count"`
		ClientInstance string `gorm:"column:client_instance"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("session_count AS session_count, client_instance AS client_instance").
		Where("device_id = ?", "idem-dev").Take(&row).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if row.SessionCount != 3 || row.ClientInstance != "inst-a" {
		t.Fatalf("重跑迁移不得重置已有数据：session_count=%d client_instance=%q", row.SessionCount, row.ClientInstance)
	}
}

// C1-4：唯一索引的列清单**必须恰好**是 (user_id, device_id)。
//
// 这条是本批次的反向红线：任何人「顺手」把 client_instance 加进索引都会红。
// 为什么这么危险：加了之后 exe 的三个会话会变成三行，而 used 的公式仍在
// 按行算 Σ max(1, session_count) ⇒ 同一批会话被数两遍 ⇒ 上限 3 的用户
// 开第 2 个会话就被拒，且账面上「占用了 2 台设备」。
func TestDeviceSlotsUniqueIndexKeepsTwoColumns(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}
	columns, ok := deviceBindingUniqueIndexColumns(t, db)
	if !ok {
		t.Fatalf("找不到唯一索引 %s", entity.AccountDeviceBindingUniqueIndex)
	}
	want := []string{"user_id", "device_id"}
	if len(columns) != len(want) {
		t.Fatalf("唯一索引必须恰好 %d 列 %v，实际 %d 列 %v —— "+
			"把 client_instance 加进索引会让 exe 的三个会话变成三行，"+
			"与 used = Σ max(1, session_count) 的口径直接打架（141 §3.2 / C1-4）",
			len(want), want, len(columns), columns)
	}
	for i := range want {
		if !strings.EqualFold(columns[i], want[i]) {
			t.Fatalf("唯一索引第 %d 列必须是 %q，实际 %q（全列 %v）", i+1, want[i], columns[i], columns)
		}
	}
	if strings.Contains(strings.ToLower(strings.Join(columns, ",")), "client_instance") {
		t.Fatalf("唯一索引**不得**包含 client_instance，实际列 %v", columns)
	}
}

// C1-4 的行为级断言：同 user + 同 device + 不同 client_instance 仍被拒。
//
// 只断言元数据列清单不够硬 —— 它证明「索引定义如此」，但证明不了
// 「索引真的在拦」。这一条直接插两行，第二行必须被唯一索引拒绝：
// 这正是「exe 三个会话共用一个 device_id」在存储层的表达（141 §5.2）。
func TestDeviceSlotsUniqueIndexRejectsSameDeviceDifferentInstance(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}
	first := entity.AccountDeviceBinding{
		UserID: 91, DeviceID: "exe-host-dev", ClientInstance: "session-1", SessionCount: 1,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	second := entity.AccountDeviceBinding{
		UserID: 91, DeviceID: "exe-host-dev", ClientInstance: "session-2", SessionCount: 2,
	}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("同 (user_id, device_id) 的不同 client_instance 必须被唯一索引拒绝 —— " +
			"放行就意味着 exe 的三个会话占三行，与 max(1,session_count) 口径打架（C1-4）")
	}
	// 反方向对照：不同 user 用同一 device + 同一 instance 必须允许（否则跨账号串扰）。
	other := entity.AccountDeviceBinding{
		UserID: 92, DeviceID: "exe-host-dev", ClientInstance: "session-1", SessionCount: 0,
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("不同账号绑定同一台设备应当允许：%v", err)
	}
}

// deviceBindingUniqueIndexColumns 取唯一索引的实际列清单。
//
// 返回值 ok=false 表示索引不存在（调用方应当报错，而不是当成「零列通过」——
// 那会让「索引被删掉」这种故障静默变绿）。
func deviceBindingUniqueIndexColumns(t *testing.T, db *gorm.DB) ([]string, bool) {
	t.Helper()
	indexes, err := db.Migrator().GetIndexes(&entity.AccountDeviceBinding{})
	if err != nil {
		t.Fatalf("GetIndexes: %v", err)
	}
	for _, index := range indexes {
		if index.Name() == entity.AccountDeviceBindingUniqueIndex {
			return index.Columns(), true
		}
	}
	return nil, false
}

// session_reported_at 的**可空性**断言（141 §5.4 补列）。
//
// ## 为什么这条必须有，而不是被上面的 deviceSlotsColumnNames 覆盖掉
//
// 上面那条只验「列存在」。而本列的**语义核心是 NULL 合法**：
//
//   - NULL = 这台设备从没上报过会话数（普通浏览器 / 老数据 / exe 没开过会话）
//     → service 层退回 last_seen_at 判心跳
//   - 非 NULL = 上报过，用它判「上报线程是否还活着」
//
// 若迁移把列写成 `NOT NULL DEFAULT <某个时刻>`，历史行会被填成同一个假时间戳：
// 那批设备的 session_count 会**立即被判成过期并归零** —— 而它们可能正开着浏览器。
//
// ## 为什么用裸 INSERT 验证
//
// GORM 的 Create 会把结构体零值显式写进 INSERT（零值 time 是 0001-01-01，
// 不是 NULL），测的就不是「列的默认值」而是「应用层写了什么」。
// 裸 INSERT 不写这一列，才是唯一能证明「DDL 允许 NULL 且初值为 NULL」的写法。
func TestDeviceSlotsSessionReportedAtIsNullable(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_reported_at") {
		t.Fatal("session_reported_at 列必须被迁移建出来（否则心跳超时永远退回 last_seen_at）")
	}

	if err := db.Exec("INSERT INTO "+entity.TableNameAccountDeviceBinding+
		" (user_id, device_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?)",
		88, "reported-at-null-dev", "2026-11-15 00:00:00", "2026-11-15 00:00:00").Error; err != nil {
		t.Fatalf("raw insert（不写 session_reported_at）必须成功，说明该列允许 NULL：%v", err)
	}

	var row struct {
		SessionReportedAt *string `gorm:"column:session_reported_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("session_reported_at AS session_reported_at").
		Where("device_id = ?", "reported-at-null-dev").Take(&row).Error; err != nil {
		t.Fatalf("read row: %v", err)
	}
	// 指针为 nil ⇒ 数据库里真的是 NULL，而不是零值时间被读出来。
	if row.SessionReportedAt != nil {
		t.Fatalf("历史行的 session_reported_at 必须是 NULL（未上报过），实际 %q", *row.SessionReportedAt)
	}
}

// session_reported_at 在**老库升级路径**上也要补出来，且补完后历史行全是 NULL。
//
// session_reported_at 在**老库升级路径**上必须被补出来，且历史行保持 NULL。
//
// ## ⚠️ 这条用例的形式是被变异验证逼出来的（初版是假钉子）
//
// 初版只建了「没有该列的老结构表」就去调 ensureAccountDeviceBindingsSchema，
// 结果**把 spec 里那一行删掉，用例仍然 PASS**。
//
// 根因：本仓的加列有**两条路径**
//
//	新库 / 标记未置位 → applyAccountDeviceBindingsSchema 里的
//	                    db.AutoMigrate(&entity.AccountDeviceBinding{})
//	                    ⇒ 列是从 **entity 字段**推出来的，与 spec 无关
//	老库 / 标记已置位 → ensureDevicePoolBindingSchema 的 spec 数组
//	                    （调用点在标记判断**之前**，L83 vs L86）
//
// 初版没有置位 v1 标记，走的正是 AutoMigrate 那条 ⇒ 删 spec 当然不变红。
//
// 因此本用例必须**同时**做三件事才能测到 spec：
//   ① 建出缺列的老结构表
//   ② 建好唯一索引（否则 ensureAccountDeviceBindingIndexes 会先失败）
//   ③ 置位 accountDeviceBindingsSchemaV1Key（否则走不到 spec 那条分支）
//
// 判据：把 spec 里 session_reported_at 那一行注释掉 → 本用例必红。
func TestDeviceSlotsSessionReportedAtBackfilledOnLegacyDB(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := db.Exec("DROP TABLE IF EXISTS " + entity.TableNameAccountDeviceBinding).Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	// 老结构：141 的前两列在（上一批已上线），但 session_reported_at 不在。
	legacyDDL := "CREATE TABLE " + entity.TableNameAccountDeviceBinding + " (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"user_id INTEGER NOT NULL, " +
		"device_id VARCHAR(191) NOT NULL, " +
		"first_seen_at DATETIME, last_seen_at DATETIME, created_at DATETIME, updated_at DATETIME, " +
		"owner_kind VARCHAR(16) NOT NULL DEFAULT 'login', " +
		"token_id INTEGER NOT NULL DEFAULT 0, " +
		"device_label VARCHAR(64) NOT NULL DEFAULT '', " +
		"revoked_at DATETIME NULL, " +
		"revoked_reason VARCHAR(32) NOT NULL DEFAULT '', " +
		"client_instance VARCHAR(64) NOT NULL DEFAULT '', " +
		"session_count INTEGER NOT NULL DEFAULT 0)"
	if err := db.Exec(legacyDDL).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX " + entity.AccountDeviceBindingUniqueIndex +
		" ON " + entity.TableNameAccountDeviceBinding + " (user_id, device_id)").Error; err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	// ③ 置位标记 —— 缺了这一步就会走 AutoMigrate，本用例变成假钉子。
	if err := setSystemSettingFlag(db, accountDeviceBindingsSchemaV1Key); err != nil {
		t.Fatalf("set legacy flag: %v", err)
	}
	// 一条历史行：补列后它必须保持 NULL（不能被填成假时间戳）。
	if err := db.Exec("INSERT INTO "+entity.TableNameAccountDeviceBinding+
		" (user_id, device_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?)",
		99, "legacy-row-dev", "2026-11-14 00:00:00", "2026-11-14 00:00:00").Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// 前置断言：确认此刻真的没有该列，否则本用例测到的不是「补列」。
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_reported_at") {
		t.Fatal("前置失效：老结构里已经有 session_reported_at，本用例测不到补列路径")
	}

	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("upgrade run: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_reported_at") {
		t.Fatal("老库（v1 标记已置位）必须补上 session_reported_at —— " +
			"加列若不在 spec 里，这些库永远没有该列、心跳超时永远退回 last_seen_at，且不报错")
	}

	var row struct {
		SessionReportedAt *string `gorm:"column:session_reported_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("session_reported_at AS session_reported_at").
		Where("device_id = ?", "legacy-row-dev").Take(&row).Error; err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if row.SessionReportedAt != nil {
		t.Fatalf("补列后历史行必须保持 NULL（填假时间戳会让那批设备的会话数立刻被判成过期），实际 %q",
			*row.SessionReportedAt)
	}
}

// session_reported_at 的 **entity 侧列定义**必须允许 NULL。
//
// ## 为什么这条与上面两条不重复（三条各测一条路径）
//
//	① TestDeviceSlotsSessionReportedAtIsNullable    —— 新库：AutoMigrate 建出的表
//	② TestDeviceSlotsSessionReportedAtBackfilled...  —— 老库：spec 数组补列
//	③ 本条                                            —— entity 的 **列定义本身**
//
// ① 会在 spec 存在时被「spec 也建了列」掩盖：实测把 entity 字段删掉，
// ①仍 PASS（spec 独立建列）。所以要抓「entity 写错」必须直接断言列定义。
//
// ## 判据
//
// 列定义必须**不含** `not null`。若写成 `NOT NULL DEFAULT <时刻>`：
//   - 历史行会被填成同一个假时间戳 ⇒ 那批设备的 session_count 立刻被判成过期并归零
//   - 而它们可能正开着浏览器
//
// 期望值不引用任何常量 —— 这条断言的是「有没有 not null 这个词」，
// 与列名/表名无关，变量改名绕不过去。
func TestDeviceSlotsSessionReportedAtColumnAllowsNull(t *testing.T) {
	// DryRun 的 MySQL 只为拼 DDL，不连库（同 client_kind 的同类用例）。
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:password@tcp(127.0.0.1:3306)/test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run mysql: %v", err)
	}

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&entity.AccountDeviceBinding{}); err != nil {
		t.Fatalf("parse entity: %v", err)
	}
	field := stmt.Schema.LookUpField("SessionReportedAt")
	if field == nil {
		// 字段被删掉也是一种「不允许 NULL」的退化：此时 spec 仍会建出可空列，
		// 但 entity 侧读写不到它，session_reported_at 永远不被写。
		t.Fatal("entity 缺少 SessionReportedAt 字段：心跳时间戳永远不会被写入，S9 退回 last_seen_at")
	}
	if field.DBName != "session_reported_at" {
		t.Fatalf("字段 DBName = %q，必须是 session_reported_at", field.DBName)
	}

	definition := strings.ToLower(db.Migrator().FullDataTypeOf(field).SQL)
	if strings.Contains(definition, "not null") {
		t.Fatalf("session_reported_at 的列定义不能有 NOT NULL（历史行必须保持 NULL），实际 %q", definition)
	}
	if !strings.Contains(definition, "datetime") {
		t.Fatalf("session_reported_at 必须是 datetime 类型，实际 %q", definition)
	}
}
