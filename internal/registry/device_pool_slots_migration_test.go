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
	"gorm.io/gorm"
)

// 141 §3.1 要求的两列。
//
// 期望值写字面量而不是引用任何常量：本批次这两列没有对应常量，
// 将来若加了常量，这里的字面量仍能在常量漂移时变红。
var deviceSlotsColumnNames = []string{"client_instance", "session_count"}

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
