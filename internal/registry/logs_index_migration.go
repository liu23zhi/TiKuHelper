package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// logs 表的复合索引迁移（R3 impossible_travel 的性能前提）。
//
// # 为什么需要它
//
// R3 的判据是「同一账号的**相邻两次**成功登录，时间间隔是否短于物理上可能的
// 旅行时间」。这条判据必须按 `user_id` 取**时间序列**，再逐对比较。
//
// `logs` 现有 5 个索引全是**单列**（user_id / username / module / create_time /
// correlation_id）。单列 `user_id` 索引能让「取出某用户的行」很快，但取出来之后
// 还要按 `create_time` 排序 —— 数据量大时会退化成「全量取出 + 内存排序」。
// 复合索引 `(user_id, create_time)` 让「同一用户的时间序列」直接被索引顺序覆盖，
// 既省排序又支持 range scan。
//
// # 为什么是 FLAG 型（HasIndex 前置判断）而不是 IF NOT EXISTS
//
// SQLite 支持 `CREATE INDEX IF NOT EXISTS`，MySQL **不支持**这个写法
// （MySQL 8 之前没有该语法；MariaDB 也没有）。而本仓同时支持两种方言，
// 所以统一用 `HasIndex` 做幂等判断 —— 与 `ensureAccessTokenQuotaEpochUniqueIndex`
// 是同一范式（那边注释也记了这条方言差异）。
//
// # 失败语义
//
// 索引缺失**不阻断**功能（R3 仍能跑，只是慢），所以这里**返回 error 但不中断启动**：
// 由调用方决定记日志。这与「结构性建表失败必须 FATAL」不同 —— 索引是性能优化，
// 不该让服务起不来。
func ensureLogsUserTimeIndex(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		// 表还没建出来（极端空库首次启动）：交给后续 AutoMigrate，下次启动重试。
		return nil
	}
	const indexName = "idx_logs_user_time"
	if db.Migrator().HasIndex(entity.TableNameLog, indexName) {
		return nil
	}
	// 两种方言都能接受的语法：不带 IF NOT EXISTS，幂等性由上方的 HasIndex 保证。
	statement := fmt.Sprintf(
		"CREATE INDEX %s ON %s (user_id, create_time)",
		indexName, entity.TableNameLog,
	)
	if err := db.Exec(statement).Error; err != nil {
		return fmt.Errorf("创建 %s 索引失败: %w", indexName, err)
	}
	logger.SysLog(fmt.Sprintf(
		"logs 复合索引已创建：%s(user_id, create_time) —— R3 impossible_travel 的时间序列查询依赖它", indexName))
	return nil
}
