package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureScriptRunSessionRevokeReasonSchema 给 script_run_sessions 补 revoked_reason 列。
//
// # 为什么需要这一列
//
// 三张表里只有本表的撤销**没有原因**：device_pool 有 revoked_reason 语义的
// 处置来源，黑名单有 kind/value，而 script_run_sessions 只有 revoked_at。
// 于是「心跳超时被抢占」「用户手动关闭」「管理员强制撤销」在数据上完全同形，
// 「我的脚本」通知区无法告诉用户到底发生了什么（2026-11-17 用户需求）。
//
// # 为什么是列检测型而不是标记型
//
// 本迁移只加列、不回填（DEFAULT ” 让存量行自动落空串，无需 UPDATE 全表），
// 重复执行只是一次 HasColumn 元数据查询，代价可忽略。
// 这与 121 §5 决策 1 的 A 规则一致：纯建表/加列 → 列检测型，不写
// system_settings 标记。标记型会在「标记已写但列被人为删掉」时永久漏建。
//
// # 空串的语义必须先定清楚
//
// 空串 = **没有撤销原因**，包含两种情形：
//   - 仍在使用 / 正常到寿（revoked_at 也是空）
//   - 本列加入之前就被撤销的历史行（revoked_at 非空但原因为空）
//
// 前端看到「revoked_at 非空 + reason 为空」时必须显示「已撤销」而**不要猜原因**。
// 猜原因比不显示更糟：猜错了用户会去排查一个不存在的问题。
func ensureScriptRunSessionRevokeReasonSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameScriptRunSession) {
		return nil
	}
	if db.Migrator().HasColumn(&entity.ScriptRunSession{}, "RevokedReason") {
		return nil
	}
	if err := db.Migrator().AddColumn(&entity.ScriptRunSession{}, "RevokedReason"); err != nil {
		return fmt.Errorf("add script run session revoked reason: %w", err)
	}
	return nil
}
