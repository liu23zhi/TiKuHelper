package entity

import "time"

// 用户自管设备拉黑表 `user_device_blocks`（137-设备体系设计 §3.3）。
//
// ## 为什么独立成表，而不是复用 account_device_bindings.revoked_reason
//
// 137 §3.3 的对照表结论（用户拍板）：
//
//	复用 revoked_reason        新建表
//	----------------------------------------------
//	一行既是「绑定历史」又是「拉黑状态」   拉黑是独立事实，删行即解除
//	解除拉黑要再改一次状态          解除 = DELETE
//	「我拉黑了哪些设备」要混在绑定表里查    直查本表
//
// 关键理由：**绑定行会被「静默重绑」清空 `revoked_at`**（拍板 B），而拉黑状态
// 必须持续存在。混在一行里，重绑就等于解除拉黑 —— 语义错误。
//
// ## 与「撤销绑定」是两件独立的事
//
//	撤销（revoked_at 非空）= 释放槽位，下次请求可能被静默重绑
//	拉黑（本表有一行）    = 拒绝使用，任何绑定状态都不会放行（137 §5 第 2 步）
//
// ⚠️ MERGE-DEDUPE（合并时必读，写给主代理）：
// 批次 A1（d137-a1，worktree devA1）在 `internal/entity/account_device_bindings.go`
// 里也声明了同一组符号（`TableNameUserDeviceBlock` / `UserDeviceBlockUniqueIndex` /
// `UserDeviceBlock`），因为 A1 的迁移 `ensureDevicePoolBindingSchema` 要
// AutoMigrate 本表。两处声明**逐字等价**（列、类型、tag、索引名全部一致）。
// 合并时保留**本文件**，删除 A1 文件里那三处 OR 反之，二者择一即可 ——
// 同时保留会 duplicate declaration 编译失败，这不是缺陷而是一次明确的择一动作。
const (
	// TableNameUserDeviceBlock 是「用户主动拉黑某台设备」的表名。
	TableNameUserDeviceBlock = "user_device_blocks"

	// UserDeviceBlockUniqueIndex 是 (user_id, device_id) 的唯一索引名。
	//
	// 它是「同一用户对同一设备只有一条拉黑记录」的幂等锚点：
	// 缺了它，用户连点两次「拉黑」会写两行，解除时删掉一行仍有另一行 →
	// 界面永远显示「已拉黑」，而且没有任何报错（与 account_device_bindings
	// 的唯一索引是同一动机）。
	UserDeviceBlockUniqueIndex = "uk_user_device_block_user_device"
)

// UserDeviceBlock 是一行「某用户主动拉黑了某台设备」。
//
// 判定流程的第 ② 步查它（137 §5）：命中即 403「你已拉黑该设备」，
// 且**用户自己可以解除**（DELETE 本行）；与全局黑名单（管理员封禁、
// 用户不可解除，`blacklist_entries.kind='device'`）严格区分。
type UserDeviceBlock struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// UserID 关联 users.id。唯一索引第 1 列，同时单独建索引供「我的拉黑列表」直查。
	UserID int32 `gorm:"column:user_id;type:int(11);not null;uniqueIndex:uk_user_device_block_user_device,priority:1;index:idx_user_device_block_user" json:"userId"`
	// DeviceID 与 AccountDeviceBinding.DeviceID 同一归一化口径
	// （service.NormalizeDevicePoolDeviceID：去首尾空白、截断到 191）。
	//
	// 191 而不是 255：MySQL utf8mb4 下索引前缀上限 767 字节，191*4=764 是安全值。
	DeviceID string `gorm:"column:device_id;type:varchar(191);not null;uniqueIndex:uk_user_device_block_user_device,priority:2" json:"deviceId"`
	// Reason 是用户填写的拉黑原因（纯展示，可空）。
	Reason string `gorm:"column:reason;type:varchar(255);not null;default:''" json:"reason"`
	// CreatedAt 是拉黑时间。本表**没有 status 列**：存在即拉黑，删行即解除 ——
	// 这正是选独立表的全部价值（137 §3.3「删行即解除」）。
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*UserDeviceBlock) TableName() string { return TableNameUserDeviceBlock }
