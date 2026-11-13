package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// login_failure_events_schema_v1 标记「R9 失败登录流水表已就绪」。
const loginFailureEventsSchemaReadyKey = "login_failure_events_schema_v1"

// EnsureLoginFailureEventSchema 建立 R9 所需的失败登录流水表与索引。
//
// 依据：task_doc/125-R9失败登录流水表设计稿.md §2 / §4。
//
// # 与 EnsureRiskSchema 的失败语义对齐（121 §4.7）
//
//   - **建表失败 → 返回 error**：R9 是安全相关规则，结构性故障必须让外层 FATAL 看见。
//     但要注意：本表的写入是**旁路**（写失败不影响登录响应），所以「表不存在」
//     不会让任何用户登录失败 —— 它只是让 R9 静默失效。正因为静默，才必须 FATAL。
//   - 建表失败**绝不写标记**（121 §2.1）：先写标记再建表，那张表就永远不会被创建，
//     且没有任何报错（标记说"已完成"）。
//
// # 索引不需要手工补建
//
// `entity.LoginFailureEvent` 的 `ClientIP` / `CreatedAt` 已带
// `index:idx_login_failure_ip_time,priority:1|2` 标签，`AutoMigrate` 会直接创建
// 复合索引 —— 这与 `risk_migration.go` 里「老库已跑过标记、新索引补不上」的情形
// 不同（那张表上线后改过 schema）。本表是全新表，随 AutoMigrate 一次到位。
//
// ⚠️ 但**不能因此省略 HasIndex 校验**：若将来有人把标签改坏（比如误删 priority），
// AutoMigrate 会静默建成两个单列索引，R9 的「按 IP + 时间窗」查询就退化成两次扫描。
// 所以这里显式核对一次复合索引是否存在，缺失就补建。
func EnsureLoginFailureEventSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来（极端空库首次启动）：交给后续 AutoMigrate，下次启动重试。
		return nil
	}
	if err := db.AutoMigrate(&entity.LoginFailureEvent{}); err != nil {
		return fmt.Errorf("建立 %s 表失败: %w", entity.TableNameLoginFailureEvent, err)
	}
	if err := ensureLoginFailureEventIndexes(db); err != nil {
		return err
	}
	// 建表与索引都成功后才写标记。
	if systemSettingFlagIsSet(db, loginFailureEventsSchemaReadyKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, loginFailureEventsSchemaReadyKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入 %s 迁移标记失败（下次启动会重跑，幂等）：%v",
			loginFailureEventsSchemaReadyKey, err))
	}
	return nil
}

// ensureLoginFailureEventIndexes 保证 (client_ip, created_at) 复合索引存在。
//
// 注意这是**普通索引**不是唯一索引：同一 IP 在同一秒可以有多条失败（撞库时必然如此），
// 唯一索引会直接挡掉写入。
func ensureLoginFailureEventIndexes(db *gorm.DB) error {
	const indexName = "idx_login_failure_ip_time"
	if db.Migrator().HasIndex(entity.TableNameLoginFailureEvent, indexName) {
		return nil
	}
	// 不带 IF NOT EXISTS：MySQL 不支持该写法，幂等性由上方 HasIndex 保证。
	statement := fmt.Sprintf(
		"CREATE INDEX %s ON %s (client_ip, created_at)",
		indexName, entity.TableNameLoginFailureEvent,
	)
	if err := db.Exec(statement).Error; err != nil {
		return fmt.Errorf("创建 %s 索引失败: %w", indexName, err)
	}
	logger.SysLog(fmt.Sprintf(
		"%s 复合索引已创建：%s(client_ip, created_at) —— R9 credential_stuffing 依赖它",
		entity.TableNameLoginFailureEvent, indexName))
	return nil
}
