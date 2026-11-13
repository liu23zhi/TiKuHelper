package entity

import "time"

// 公告系统（N4，task_doc/112 §4.3 / 113 §8.1 / 121 §4.5）。
//
// 两种公告的**唯一**区别是 Kind：
//
//	normal    → 只在 TIKU WebUI 显示（「通知你一件事」）
//	incident  → WebUI 显示 + 随每一次搜题响应下发给 PLUS（「服务已经坏了」）
//
// 故障公告不是「推送给 PLUS」，而是 PLUS 每次搜题时从响应里拿到 —— 113 §1.1
// 明确要求它**不能影响搜题**（PLUS 侧是 swal 弹一次，不阻塞、不 await）。
// 因此本表只是「有哪些公告正在生效」的数据源，下发逻辑在
// service.ActiveIncidents，不在实体里。
const (
	TableNameAnnouncement = "announcements"

	// AnnouncementKindNormal 只在 WebUI 显示，绝不下发到搜题响应。
	AnnouncementKindNormal = "normal"
	// AnnouncementKindIncident 额外随搜题响应下发（113 §3.1：只下发 incident）。
	AnnouncementKindIncident = "incident"

	// Severity 只对 incident 有意义，决定 PLUS 侧弹窗的图标与措辞强度。
	AnnouncementSeverityInfo     = "info"
	AnnouncementSeverityWarning  = "warning"
	AnnouncementSeverityCritical = "critical"
)

// Announcement 是一条公告。
//
// 为什么用 StartsAt/EndsAt 而不是只靠 Enabled（112 §4.3）：
//   - 可以提前建好公告，到点自动生效（不必有人守着点「启用」）；
//   - 故障公告可以预设结束时间，避免忘记关掉导致 PLUS 一直弹。
//
// ⚠️ Enabled 上**刻意不写 `default:` 标签**（与 blacklist_entry.go 的写法不同）：
// 带 `default:true` 时 GORM 会把「等于默认值」的字段从 INSERT 里省略
// （本仓已在 review_rebate / blacklist 上实测踩过），于是 `Enabled: false`
// 会被静默写成 true —— 一条「停用的故障公告」照样进搜题响应。
// 不写 default、只写 `not null` 时 GORM 始终把该列带进 INSERT，零值语义确定。
//
// ⚠️ 时间一律 time.Time（不含字符串日期），存库前在 service 层统一归一到 UTC：
// SQLite 把时间按字符串比较，只有写入侧与查询侧用同一个 location 时
// 「窗口是否生效」的判定才可靠（混用 +08:00 与 Z 会让字符串比较失真）。
type Announcement struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`

	// Kind 二值，见 AnnouncementKind*。索引 (kind, enabled) 供下发查询按类型筛。
	//
	// 保留 `default:'normal'`：GORM 只在字段为**零值**（""）时才省略该列，
	// 而 service 层归一化后永远是非零的合法值，所以这里不会出现「想写 incident
	// 却被写成 normal」。DB 侧默认值只作为兜底 —— 直接 INSERT 不带 kind 时
	// 落 normal（最安全的那一个：不下发到 PLUS）。
	Kind string `gorm:"column:kind;type:varchar(16);not null;default:'normal';index:idx_announcement_kind_enabled,priority:1" json:"kind"`

	Title   string `gorm:"column:title;type:varchar(200);not null" json:"title"`
	Content string `gorm:"column:content;type:text;not null" json:"content"`

	// Severity 取 info / warning / critical，只对 incident 有意义。
	Severity string `gorm:"column:severity;type:varchar(16);not null;default:'info'" json:"severity"`

	// Enabled 是总开关（人工开关），与 StartsAt/EndsAt 的时间窗相互独立：
	// 两者都满足才算「正在生效」。
	Enabled bool `gorm:"column:enabled;not null;index:idx_announcement_kind_enabled,priority:2;index:idx_announcement_window,priority:1" json:"enabled"`

	// StartsAt 是展示窗口起点（含）。索引 (enabled, starts_at, ends_at) 供时间窗查询。
	StartsAt time.Time `gorm:"column:starts_at;not null;index:idx_announcement_window,priority:2" json:"startsAt"`

	// EndsAt 为 nil 表示永久有效（见 112 §4.3）。
	EndsAt *time.Time `gorm:"column:ends_at;index:idx_announcement_window,priority:3" json:"endsAt,omitempty"`

	// CreatedBy 是发布管理员 ID（0 = 系统/未知，例如迁移或脚本发布）。
	CreatedBy int32     `gorm:"column:created_by;not null" json:"createdBy"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*Announcement) TableName() string { return TableNameAnnouncement }
