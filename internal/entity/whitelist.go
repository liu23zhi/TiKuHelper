package entity

import "time"

// TableNameWhitelistEntry 是白名单规则表（task_doc/123「请新增白名单管理，规则模仿黑名单。
// 优先级 白名单 > 黑名单」）。
const TableNameWhitelistEntry = "whitelist_entries"

// WhitelistEntry 是一条白名单规则。
//
// 列结构与 BlacklistEntry **逐列对齐**（kind / pattern / value_hash / value_masked /
// match_type / reason / enabled / expires_at / created_by / 两个时间戳 + 两级索引），
// 这样两个管理页与两组 service 函数能保持同一套语义，运维不需要记两套字段。
//
// 与黑名单的三处**刻意差异**（不是遗漏）：
//
//  1. 只有 Kind=ip 一种粒度。黑名单的 phone / wechat / region 是「谁能访问」的
//     阻断维度，白名单存在的唯一理由是「这条请求必须绕过黑名单」——中间件看到的
//     只有客户端 IP，其它粒度没有落点。多存几种粒度只会得到「看着已配置、永不命中」
//     的规则，那比明确拒绝更糟（与 BL-1 拒绝 region 的理由同源）。
//  2. **没有总开关**。白名单是「放行」语义：加一条就该立刻生效。给它配一个
//     `whitelist_enabled` 开关，会多出「运营加了规则却没生效」这个故障面，
//     而误放行的风险本来就由运营自己在填规则时承担。
//  3. 没有配对的命中日志表。白名单命中等于「本次请求不拦截」，按判据
//     「白名单内 IP 不记 blacklist_hits 行」，它连黑名单的留痕都不产生，
//     自然也不需要一张自己的命中表（否则等于把「没拦」这一事实写成噪音）。
//
// ⚠️ Enabled 的零值陷阱与 BlacklistEntry 完全相同：带 `default:true` 标签时
// GORM 会把等于默认值的字段从 INSERT 里省略，于是 `Create(&entry{Enabled:false})`
// 会被静默写成 true（连 `Select("*")` 也绕不过）。新建一条停用规则必须走
// 「Create 之后 Update enabled=false」两步，见 service.CreateWhitelistEntryWithDB。
type WhitelistEntry struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// Kind 目前只能是 ip（其它值在 service 层被拒绝，见 ErrInvalidWhitelistKind）。
	Kind string `gorm:"column:kind;type:varchar(16);not null;default:'';uniqueIndex:uk_whitelist_entry_pattern,priority:1;index:idx_whitelist_entry_kind_enabled,priority:1" json:"kind"`
	// Pattern 是归一化后的匹配模式（单个 IP 或 CIDR）。
	Pattern string `gorm:"column:pattern;type:varchar(191);not null;default:''" json:"pattern"`
	// ValueHash 是 sha256(Pattern) 的十六进制串，唯一索引与查询都走它。
	ValueHash string `gorm:"column:value_hash;type:char(64);not null;default:'';uniqueIndex:uk_whitelist_entry_pattern,priority:2" json:"valueHash"`
	// ValueMasked 是展示用脱敏值（203.0.113.* ；CIDR 原样）。
	ValueMasked string `gorm:"column:value_masked;type:varchar(191);not null;default:''" json:"valueMasked"`
	// MatchType 取 exact / cidr；prefix 只是输入侧简写，归一化后一律变成 cidr。
	MatchType string `gorm:"column:match_type;type:varchar(16);not null;default:'exact';uniqueIndex:uk_whitelist_entry_pattern,priority:3" json:"matchType"`
	// Reason 是放行原因（必填：日后要能解释「这个网段为什么不必拦」）。
	Reason string `gorm:"column:reason;type:varchar(255);not null;default:''" json:"reason"`
	// Enabled 单条开关，默认 true（临时停用比删除更常用，删了就丢审计）。
	Enabled bool `gorm:"column:enabled;not null;default:true;index:idx_whitelist_entry_kind_enabled,priority:2" json:"enabled"`
	// ExpiresAt 为 nil 表示永久；到期的条目**不再放行**但保留记录。
	ExpiresAt *time.Time `gorm:"column:expires_at" json:"expiresAt,omitempty"`
	// CreatedBy 是操作管理员 ID（0 = 系统导入）。
	CreatedBy int32     `gorm:"column:created_by;type:int(11);not null;default:0" json:"createdBy"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*WhitelistEntry) TableName() string { return TableNameWhitelistEntry }
