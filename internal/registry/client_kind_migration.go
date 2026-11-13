package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ClientKindColumn 是 logs 与 access_tokens 共用的物理列名。
//
// 列名由 task_doc/107 Q11a 定案为 client_kind（与既有 snake_case 一致），
// 不叫 clientType / requestSource —— requestSource 已被 AI 兜底接口占用。
const ClientKindColumn = "client_kind"

// ClientKindUnknown 既是列默认值，也是判定不出来时的兜底值。
//
// 三值集合由 task_doc/111 §6.2 定案：exam_client / extension / unknown。
// 明确**没有** exe_non_exam —— 用户口径是「来源只有 exe 客户端（只算考试模式）
// 和扩展」，因此 exe 只用于考试，不存在第四种取值。
const ClientKindUnknown = "unknown"

// clientKindColumnDDL 是 SQLite 方言下的列定义。
//
// NOT NULL DEFAULT 'unknown' 是 task_doc/121 决策 5 的硬要求：
// 新列若 NOT NULL 且无默认值，旧代码 INSERT 不带该列就会失败
// （MySQL 严格模式直接报错）。带默认值则旧代码完全不受影响。
//
// 默认值同时承担 Q11d 的「老数据回填 unknown（不猜）」——
// ADD COLUMN 时存量行自动落 default，无需 UPDATE 全表，
// 因此不存在「逐行扫大表」的启动性能风险。
const clientKindColumnDDL = ClientKindColumn + " varchar(32) NOT NULL DEFAULT '" + ClientKindUnknown + "'"

// clientKindLogColumn 只承载 logs.client_kind 的列定义，供 MySQL 分支的
// Migrator().AddColumn 取用。
//
// ⚠️ 为什么需要这个影子结构：internal/entity/logs.gen.go 是 gorm.io/gen
// 生成文件（DO NOT EDIT），entity.Log 上没有 ClientKind 字段，
// `db.Migrator().AddColumn(&entity.Log{}, "ClientKind")` 会因 LookUpField
// 返回 nil 而失败。所以不能靠 AutoMigrate 加字段，必须显式 ALTER；
// 而 MySQL 分支需要一份「字段定义来源」，就用这个仅映射列的子集结构。
//
// 读侧同样不依赖实体字段：用 Table("logs").Select("client_kind") 直接取列。
type clientKindLogColumn struct {
	ID int32 `gorm:"column:id;primaryKey"`
	// not null + default:unknown 是这个列的全部契约，缺一不可（见 clientKindColumnDDL）。
	ClientKind string `gorm:"column:client_kind;type:varchar(32);not null;default:unknown"`
}

// TableName 把影子结构钉回 logs，避免落到 gorm 的默认复数表名。
func (*clientKindLogColumn) TableName() string { return entity.TableNameLog }

// clientKindAccessTokenColumn 同理承载 access_tokens.client_kind 的列定义。
//
// ⚠️ internal/entity/access_token.go 是手写实体，但已被 service / controller /
// 多个迁移依赖；本轮只读它的列定义，不在其中加字段 —— 列由本迁移显式 ALTER 加出。
type clientKindAccessTokenColumn struct {
	ID         int32  `gorm:"column:id;primaryKey"`
	ClientKind string `gorm:"column:client_kind;type:varchar(32);not null;default:unknown"`
}

// TableName 把影子结构钉回 access_tokens。
func (*clientKindAccessTokenColumn) TableName() string { return entity.TableNameAccessToken }

// clientKindTargets 是本迁移要处理的两张表。
//
// 两张表都加同名列：access_tokens 存「签发来源」（长期凭证，来源固定），
// logs 存「实际请求来源」（写入时冗余存，避免查询期 JOIN —— 107 Q11c 选 A）。
// 这是 task_doc/111 §6.3 的建议 C（两者都有）。
func clientKindTargets() []struct {
	table string
	proto any
} {
	return []struct {
		table string
		proto any
	}{
		{table: entity.TableNameLog, proto: &clientKindLogColumn{}},
		{table: entity.TableNameAccessToken, proto: &clientKindAccessTokenColumn{}},
	}
}

// EnsureClientKindSchema 给 logs 与 access_tokens 各补一列 client_kind。
//
// 纪律（沿用仓库既有 ensure* 约定，见 task_doc/121 §2 的 8 条规范）：
//   - 开头 db == nil 直接返回（本函数是导出函数，可能被测试/工具以 nil 调用）；
//   - 先 HasTable 确认表存在，再 HasColumn 判幂等 —— 双重检查，任一不满足都不重复加列；
//   - 结构性失败返回错误，让启动 FATAL 看见（缺列会让「调用来源」功能
//     100% 失效且无报错，正是 121 §5 决策 2 说的「更难发现」的那类故障）；
//   - 外层 recover 兜底，脏 schema 不能让服务反复崩溃；
//   - 只建列不回填：DEFAULT 'unknown' 已让存量行落 unknown，无需 UPDATE。
//
// ⚠️ 本迁移是**列检测型**，不写 system_settings 标记（121 §5 决策 1 的 A 规则：
// 纯建表/加列 → 列检测型）。理由是它没有任何数据回填，重复执行只是一次
// HasColumn 元数据查询，代价可忽略；而标记型会在「标记已写但列被人为删掉」时
// 永久漏建。结构存在即完成。
func EnsureClientKindSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("调用来源字段迁移异常: %v", recovered)
		}
	}()
	for _, target := range clientKindTargets() {
		if !db.Migrator().HasTable(target.table) {
			// 极端空库首次启动：表还没建出来（交给各自的 AutoMigrate），
			// 加列无从谈起，也不写任何标记，下次启动自然补齐。
			continue
		}
		if db.Migrator().HasColumn(target.table, ClientKindColumn) {
			continue
		}
		if addErr := addClientKindColumn(db, target.table, target.proto); addErr != nil {
			return addErr
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上 %s 列（取值 %s / extension / %s，默认 %s）",
			target.table, ClientKindColumn, ClientKindExamClientValue, ClientKindUnknown, ClientKindUnknown))
	}
	return nil
}

// ClientKindExamClientValue 与 service 包的三值保持一致。
//
// registry 不能 import service（service 已经反向 import registry/dbqueue，
// 会形成环），所以这里另立一份常量，并在测试里锁定字面值，
// 防止两边漂移。
const ClientKindExamClientValue = "exam_client"

// addClientKindColumn 按方言补列。
//
// SQLite 走裸 DDL：glebarez/sqlite 的 Migrator().AddColumn 会先做一次
// HasColumn 元数据查询再拼同样的 ALTER，多一次往返且行为一致
// （task_doc/121 §1.5 / 决策 4 的实测结论）；而 ALTER COLUMN 那条
// 「整表 copy-rename」的坑只影响 AlterColumn，与本路径无关。
//
// MySQL/MariaDB 走 Migrator().AddColumn：列类型/默认值来自影子结构的 tag，
// 与 user_archive_migration.go:94 的方言分支完全同构。
func addClientKindColumn(db *gorm.DB, table string, proto any) error {
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + table + " ADD COLUMN " + clientKindColumnDDL
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("新增 %s.%s 失败: %w", table, ClientKindColumn, err)
		}
		return nil
	}
	if err := db.Migrator().AddColumn(proto, "ClientKind"); err != nil {
		return fmt.Errorf("新增 %s.%s 失败: %w", table, ClientKindColumn, err)
	}
	return nil
}
