package registry

// 设备体系批次 A1 的表结构扩展（137 §3.1 / §3.2 / §3.3）。
//
// 与 account_device_bindings_migration.go 的分工：
//   前者（既有）建立 account_device_bindings 表本体 + 唯一索引，是一次性的
//   FLAG 型迁移（键 account_device_bindings_schema_v1）。它已经上线过，
//   标记可能已在老库里写入 —— 因此**不能**把本批次的加列塞进它里面：
//   FLAG 已置位的库会直接 return，新增的列永远补不上，而且不报任何错。
//
// 本文件是**列检测型**迁移（不是 FLAG 型）：每一列单独判 HasColumn，
// 缺了就 ALTER。这样它恰好补上「老库已跑过 v1 标记」这个缺口，
// 也与本仓的列检测型迁移（ensureUserArchiveSchema / EnsureTicketAttachmentRetentionSchema）同构。
//
// 四条纪律（与其它迁移一致）：
//  1. 结构性失败返回 error，让启动流程看见（本迁移在 db.go 里只 SysLog 降级，
//     因为设备池是权益限制不是安全边界，见 account_device_bindings_migration.go 的说明）；
//  2. 没有数据回填，不做任何 DROP；
//  3. 幂等：两次运行不重复加列、不报错；
//  4. nil db 直接返回 nil。

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// devicePoolBindingColumnSpec 是一条要补建的列。
//
// ddl 是**方言无关的最小片段**（MySQL 与 SQLite 都接受）：
// 刻意不写 CHARACTER SET / COLLATE / COMMENT —— 那些在 SQLite 上语法错误，
// 而本仓的单测全跑 SQLite（只有 MariaDB 集成测试跑 MySQL）。
type devicePoolBindingColumnSpec struct {
	column string
	ddl    string
}

// devicePoolBindingColumnSpecs 是 137 §3.2 要求的 5 列 + 141 §3.1 要求的 2 列。
//
// 逐列对上 137 的字段表（顺序与文档一致）：
//
//	owner_kind     VARCHAR(16)  NOT NULL DEFAULT 'login'   login | token
//	token_id       INT          NOT NULL DEFAULT 0
//	device_label   VARCHAR(64)  NOT NULL DEFAULT ''（文档写 DEFAULT ''，见下方说明）
//	revoked_at     DATETIME     NULL
//	revoked_reason VARCHAR(32)  NOT NULL DEFAULT ''
//
// 第二批（141 §3.1）追加两列：
//
//	client_instance VARCHAR(64) NOT NULL DEFAULT ''   这次运行的实例标识（R3）
//	session_count   INT         NOT NULL DEFAULT 0    exe 当前活跃会话数（R6/R7）
//
// ⚠️ 与 137 的一处**刻意偏差**：文档 §3.2 里 device_label 写的是
// `VARCHAR(64) NOT NULL DEFAULT ''`（在代码块里），而任务书写的是
// `device_label(VARCHAR64)` 未提默认值。这里按文档取 `NOT NULL DEFAULT ''`，
// 理由：既有行的 label 只能是空串（没人报过），而允许 NULL 会让「未上报」
// 与「上报了空串」两种状态在 Go 侧都要判指针，无收益。
//
// 为什么 revoked_at 必须是 NULL 而不是零值时间：`revoked_at IS NULL` 是
// 「占槽位」的唯一判据（137 §3.2），若用 '0000-00-00' 或零值时间，
// MySQL 严格模式会直接报错，SQLite 则会静默接受 —— 两地行为不一致，
// 而这类差异只在生产暴露。
//
// ⚠️ session_count 的默认值必须是 0 而不是「上次的峰值」类语义：
// `used = Σ max(1, session_count)` 要在「exe 全关」时回落到 1（R7），
// 而回落的前提就是这一列能真的回到 0。把默认值或写入口做成「只增不减」，
// R7 会静默失效且没有任何报错（判据 S4 就钉这一条）。
var devicePoolBindingColumnSpecs = []devicePoolBindingColumnSpec{
	{column: "owner_kind", ddl: "owner_kind VARCHAR(16) NOT NULL DEFAULT 'login'"},
	{column: "token_id", ddl: "token_id INTEGER NOT NULL DEFAULT 0"},
	{column: "device_label", ddl: "device_label VARCHAR(64) NOT NULL DEFAULT ''"},
	{column: "revoked_at", ddl: "revoked_at DATETIME NULL"},
	{column: "revoked_reason", ddl: "revoked_reason VARCHAR(32) NOT NULL DEFAULT ''"},
	// 第二批（141 §3.1）。
	{column: "client_instance", ddl: "client_instance VARCHAR(64) NOT NULL DEFAULT ''"},
	{column: "session_count", ddl: "session_count INTEGER NOT NULL DEFAULT 0"},
}

// ensureDevicePoolBindingSchema 给 account_device_bindings 补 A1 的 5 列 +
// 141 的 2 列，并建 user_device_blocks 表（137 §3.3）。
//
// 顺序说明：**先建 user_device_blocks 表，再加列**。理由是加列失败会让整个函数
// 返回 error 并跳过表创建；而 user_device_blocks 是独立的判定分支（137 §5 第 2 步），
// 它到位后即使绑定表加列失败，判定链也不会因为「表不存在」而误拒。
// （devicePoolUserBlocked 对表缺失是 fail-open 的，但少一个降级分支总是更好。）
//
// ⚠️ 本函数**不碰唯一索引**。uk_account_device_binding_user_device 必须恒为
// (user_id, device_id) 两列（141 §3.2）：把 client_instance 加进去会让 exe 的
// 三个会话变成三行，与 used = Σ max(1, session_count) 的口径直接打架 ——
// 那时三个会话各占 1 行、session_count 又各自计一次，同一批会话被数了两遍。
func ensureDevicePoolBindingSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 主表还没有的话什么都不做：本迁移只补列，建表归
	// ensureAccountDeviceBindingsSchema（db.go 里它排在本迁移之前）。
	// 若在这里 AutoMigrate 建表，等于把「建表 + 建索引」的整套纪律
	// 复制一份，两处会漂移。
	if !db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		return nil
	}

	// ① 用户自管拉黑表（137 §3.3）。
	if err := db.AutoMigrate(&entity.UserDeviceBlock{}); err != nil {
		return fmt.Errorf("create user_device_blocks fail: %w", err)
	}
	if err := ensureUserDeviceBlockIndexes(db); err != nil {
		return err
	}

	// ② 绑定表 7 列（A1 的 5 列 + 141 的 2 列）。
	for _, spec := range devicePoolBindingColumnSpecs {
		if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, spec.column) {
			continue
		}
		if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding + " ADD COLUMN " + spec.ddl).Error; err != nil {
			return fmt.Errorf("add %s.%s fail: %w", entity.TableNameAccountDeviceBinding, spec.column, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上列 %s（设备体系 A1）", entity.TableNameAccountDeviceBinding, spec.column))
	}
	return nil
}

// ensureUserDeviceBlockIndexes 保证拉黑表的 (user_id, device_id) 唯一索引存在。
//
// 与 ensureAccountDeviceBindingIndexes 同一动机、同一写法：
// 唯一索引是「同一用户对同一设备只有一条拉黑记录」的幂等锚点。
// 缺了它，用户连点两次「拉黑」会写两行，解除时删掉一行仍有另一行 → 永远是「已拉黑」。
func ensureUserDeviceBlockIndexes(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	spec := accountDeviceBindingIndexSpec{
		name:    entity.UserDeviceBlockUniqueIndex,
		table:   entity.TableNameUserDeviceBlock,
		columns: []string{"user_id", "device_id"},
		unique:  true,
	}
	if db.Migrator().HasIndex(spec.table, spec.name) {
		return nil
	}
	statement := accountDeviceBindingIndexDDL(db.Dialector.Name(), spec)
	if statement == "" {
		return fmt.Errorf("不支持的数据库方言 %q：无法为 %s 建索引 %s", db.Dialector.Name(), spec.table, spec.name)
	}
	if err := db.Exec(statement).Error; err != nil {
		// 并发启动的两个实例可能同时判定「索引不存在」，其中一个会撞 already exists。
		if db.Migrator().HasIndex(spec.table, spec.name) {
			return nil
		}
		return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
	}
	logger.SysLog(fmt.Sprintf("已为 %s 补上索引 %s", spec.table, spec.name))
	return nil
}
