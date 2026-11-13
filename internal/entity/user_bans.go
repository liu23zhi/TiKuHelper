package entity

import "time"

// TableNameUserBan 是账号封禁记录表（121 §4.8 / 121 决策 6）。
//
// 命名照本仓约定：常量名用**单数**（TableNameRiskRule / TableNameBlacklistEntry），
// 表名用**复数**。迁移登记（registry/db.go）与 service 的双读判定都引用这个常量，
// 因此它必须只有一个出处 —— 任何一处手写 "user_bans" 都可能悄悄漂移成另一张表，
// 而表现是「封禁记录写进去了，判定却永远读不到」。
const TableNameUserBan = "user_bans"

// UserBan 是一条账号封禁记录。
//
// # 为什么在 users.status 之外还要一张表
//
// `users.status='banned'` 只有一个布尔态，回答不了封禁运维必须回答的三个问题：
//
//	谁封的     → BannedBy
//	为什么封   → Reason
//	封到什么时候 → ExpiresAt（空 = 永久）
//
// 46 号文档 §5 的口径是「本期只手动封禁」，但用户 2026-11-10 追加了
// 「自动解除（临时封禁 N 分钟）与永不解除（永久封禁）两种并存」，因此
// ExpiresAt 从第一天就是语义的一部分，不能后补 —— 后补会让存量封禁记录
// 全部落成「永久」，把临时封禁悄悄升级成永久封禁。
//
// # 与 users.status 的关系：双读，不是替代
//
// 本表**不替代** users.status：判定任一侧命中即封禁（service/user_ban.go 的
// IsBannedAt，121 决策 6）。两处都写入是本仓库有意的冗余（status 是既有
// 准入谓词的读取点，不能为了一张新表去改所有调用方）。
//
// # 不建外键（仓内约定）
//
// user_id 只是逻辑关联：本仓整体不使用 FOREIGN KEY（见 121 §2 与既有全部
// 迁移），原因有三：① SQLite 的外键约束默认关闭，启用与否取决于 DSN，
// 生产与测试会表现不一致；② 账号归档是软删除（users.deleted_at），
// 外键约束对软删除毫无意义；③ 封禁记录是**审计数据**，账号被归档后
// 仍然要能回看「这个账号当年被谁封过」。
type UserBan struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// UserID 关联 users.id（逻辑关联，不建外键，见类型注释）。
	//
	// 它与 ExpiresAt 组成索引 idx_user_ban_user_expires：判定路径是
	// 「按用户取未过期行」，只按 user_id 过滤后再在内存里筛到期态。
	UserID int32 `gorm:"column:user_id;type:int(11);not null;index:idx_user_ban_user_expires,priority:1" json:"userId"`
	// Reason 是封禁原因（必填，运营要能向用户解释「为什么被封」）。
	// 46 号对黑名单规则的 reason 就是必填口径，封禁沿用同一条：留空等于
	// 事后无人能复盘，因此 service 的 Ban 会拒绝空白原因，不做静默补默认值。
	Reason string `gorm:"column:reason;type:varchar(255);not null;default:''" json:"reason"`
	// BannedAt 是封禁**生效**时间。
	//
	// 与 CreatedAt 的区别不是冗余：
	//   - BannedAt 是业务时间（当前实现等于写入时刻，将来支持「预约封禁」或
	//     「按违规时刻回溯封禁」时它才是真实生效点）；
	//   - CreatedAt 是行的物理写入时间（审计用，autoCreateTime 自动填）。
	//
	// 把它与 CreatedAt 合并会让「回溯封禁」永远无法表达 —— 而那恰恰是
	// 风控事后处置最常见的形态。
	//
	// 单独建索引 idx_user_ban_banned_at：管理页按封禁时间倒序翻页，
	// 以及将来的保留期清理（按时间删）都走它。
	BannedAt time.Time `gorm:"column:banned_at;not null;index:idx_user_ban_banned_at,priority:1" json:"bannedAt"`
	// ExpiresAt 为空 = **永久封禁**；非空 = 到期自动解除（用户 2026-11-10 定案
	// 「自动解除与永不解除两种并存」）。
	//
	// ⚠️ 必须是指针（可空列）。用零值 time.Time 表示「永久」会让 0001-01-01
	// 这种明显不是业务时间的值进入判定，而 `expires_at > now` 对它恒为真 ——
	// 看起来像永久封禁，实际是一条永远解释不通的脏数据。
	//
	// ⚠️ 到期**不删除行**：删了就丢掉「谁封的、为什么封」。到期只让判定失效，
	// 记录永久保留（与 blacklist_entries.expires_at 是同一套口径）。
	ExpiresAt *time.Time `gorm:"column:expires_at" json:"expiresAt,omitempty"`
	// BannedBy 是执行封禁的管理员 users.id；0 = 系统自动封禁（风控/黑名单触发）。
	//
	// 用 0 而不是 NULL 表示「系统」：判定与展示都不必处理三态，
	// 与 created_by / operator_user_id 等既有审计列的口径一致。
	BannedBy  int32     `gorm:"column:banned_by;type:int(11);not null;default:0" json:"bannedBy"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserBan) TableName() string { return TableNameUserBan }
