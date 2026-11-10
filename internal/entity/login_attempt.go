package entity

import "time"

const TableNameLoginAttempt = "login_attempts"

// 登录限流的两个计数维度（45-*.md §0.1.4）。
//
// 用一张表 + scope 区分两套限流，而不是两张表：两者的「连续失败计数 + 窗口 +
// 锁定」逻辑完全相同，只有配置来源与是否可关闭不同。分两张表会把同一段
// 计数逻辑抄两遍，而这类「同一语义两份实现」正是本项目踩坑高发区。
const (
	// LoginAttemptScopeAccount 是**普通模式**：按账号（username）计数。
	// 可被管理员通过 login_rate_limit_enabled 完全关闭。
	LoginAttemptScopeAccount = "account"
	// LoginAttemptScopeIP 是**被拉黑 IP 的管理员入口**：按 IP 计数，不区分账号。
	//
	// ⚠️ 这个维度**没有任何 enabled 开关**，也不允许被关闭 —— 见
	// service/login_rate_limit.go 顶部「不允许关闭」的说明。
	LoginAttemptScopeIP = "ip"
)

// LoginAttempt 是一次「连续登录失败」的计数与锁定状态（45-*.md §0.1.4 七）。
//
// 主键是 (scope, subject_key)，即规格里的「唯一索引 (scope, subject_key)」——
// 复合主键天然保证唯一，不需要再建一条唯一索引。
//
// ⚠️ 本表**刻意没有任何 bool 列**：PR #42 踩过 GORM `default:true` 会把 false
// 静默写成 true 的零值陷阱（`Select("*")` 也绕不过）。没有 bool 列就不存在该风险。
// 开关状态一律存在 system_settings 的字符串里，不落到本表。
type LoginAttempt struct {
	// Scope 取 account / ip。
	Scope string `gorm:"column:scope;type:varchar(16);not null;primaryKey" json:"scope"`
	// SubjectKey 是账号归一化值（username）或 IP 原文。
	//
	// 长度 128 而非 64：账号维度要容纳 username（本项目 varchar(255)，
	// 但登录输入的 username 会先 TrimSpace；128 足够覆盖实际用户名，
	// 超长输入由归一化函数按字符截断，不会写坏行）。
	SubjectKey string `gorm:"column:subject_key;type:varchar(128);not null;primaryKey" json:"subjectKey"`
	// FailCount 是**从最后一次成功之后**开始的连续失败次数（不是窗口内累计）。
	//
	// 语义见 45-*.md §0.1.4 三：登录成功时清零，窗口过期时重置为 1。
	FailCount int `gorm:"column:fail_count;not null;default:0" json:"failCount"`
	// WindowStart 是当前计数窗口的起点。
	//
	// ⚠️ 它在**成功时也重置**（不只是失败时）—— 否则「连续」不成立：
	// 失败 9 次 → 成功 1 次 → 若不重置窗口起点，接下来 1 次失败就会被算进
	// 旧窗口而立刻触发锁定。
	WindowStart *time.Time `gorm:"column:window_start" json:"windowStart,omitempty"`
	// LockedUntil 为 nil 表示未锁定；非 nil 且晚于当前时间表示锁定中。
	LockedUntil *time.Time `gorm:"column:locked_until;index" json:"lockedUntil,omitempty"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*LoginAttempt) TableName() string { return TableNameLoginAttempt }
