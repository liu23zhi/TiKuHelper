package entity

import "time"

const (
	TableNameBlacklistEntry = "blacklist_entries"
	TableNameBlacklistHit   = "blacklist_hits"
)

// BlacklistEntry 是一条黑名单规则（46-黑名单设计.md §2.1）。
//
// 四种粒度共用一张表，靠 Kind + MatchType 区分：
//
//	Kind=ip     + MatchType=exact  → Pattern 是规范化后的单个 IP
//	Kind=ip     + MatchType=cidr   → Pattern 是 CIDR（"203.0.113.0/24"、"2001:db8::/32"）
//	Kind=phone  + MatchType=exact  → Pattern 是 11 位大陆手机号
//	Kind=wechat + MatchType=exact  → Pattern 是 TrimSpace 后的 OpenID（大小写敏感）
//	Kind=region + MatchType=prefix → Pattern 是归属地展示串（BL-4 才接入匹配）
//
// ⚠️ 本表**不明文存原始值以外的东西**：Pattern 是归一化后的匹配模式（运营需要能看懂），
// ValueHash 是它的 sha256（查询与唯一索引走这个），ValueMasked 是展示用脱敏值。
//
// ⚠️ Enabled 的零值陷阱（与 review_rebate.go 同一个坑，本仓已实测确认）：
// 带 `default:true` 标签时 GORM 会把**等于默认值的字段从 INSERT 里省略**，
// 于是 `Create(&entry{Enabled: false})` 会被静默写成 true —— 连 `Select("*")` 也绕不过
// （GORM 在 ConvertToCreateValues 阶段就跳过了该字段）。
//
// 处置：默认值按规格保留 true（§2.1「Enabled 默认 true，单条可临时停用」），
// 但**新建一条停用规则**必须走「Create 之后 Update enabled=false」两步
// （BL-5 管理页与 BL-6 导入都必须这么做；测试 helper seedBlacklistEntry 就是这么写的）。
// 已实测：Update 能把 enabled 落成 false，缓存刷新后立即不生效。
type BlacklistEntry struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// Kind 取 ip / phone / wechat / region。
	Kind string `gorm:"column:kind;type:varchar(16);not null;default:'';uniqueIndex:uk_blacklist_entry_pattern,priority:1;index:idx_blacklist_entry_kind_enabled,priority:1" json:"kind"`
	// Pattern 是归一化后的匹配模式（IP / CIDR / 11 位手机号 / OpenID / 地区串）。
	Pattern string `gorm:"column:pattern;type:varchar(191);not null;default:''" json:"pattern"`
	// ValueHash 是 sha256(Pattern) 的十六进制串，唯一索引与查询都走它。
	ValueHash string `gorm:"column:value_hash;type:char(64);not null;default:'';uniqueIndex:uk_blacklist_entry_pattern,priority:2" json:"valueHash"`
	// ValueMasked 是展示用脱敏值（203.0.113.* / 138****5678 / o***ABC / 云南*）。
	ValueMasked string `gorm:"column:value_masked;type:varchar(191);not null;default:''" json:"valueMasked"`
	// MatchType 取 exact / cidr / prefix；ip 的 prefix 输入会被归一化成 cidr（见 service 层）。
	MatchType string `gorm:"column:match_type;type:varchar(16);not null;default:'exact';uniqueIndex:uk_blacklist_entry_pattern,priority:3" json:"matchType"`
	// Reason 是拉黑原因（必填，运营要能解释），由管理端校验。
	Reason string `gorm:"column:reason;type:varchar(255);not null;default:''" json:"reason"`
	// Enabled 单条开关，默认 true（临时停用比删除更常用，删了就丢审计）。
	Enabled bool `gorm:"column:enabled;not null;default:true;index:idx_blacklist_entry_kind_enabled,priority:2" json:"enabled"`
	// ExpiresAt 为 nil 表示永久；到期的规则不拦截但**不删除记录**。
	ExpiresAt *time.Time `gorm:"column:expires_at" json:"expiresAt,omitempty"`
	// CreatedBy 是操作管理员 ID（0 = 系统导入，后续 BL-6）。
	CreatedBy int32     `gorm:"column:created_by;type:int(11);not null;default:0" json:"createdBy"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*BlacklistEntry) TableName() string { return TableNameBlacklistEntry }

// BlacklistHit 是一次命中记录（46-黑名单设计.md §2.2）。
//
// 这张表是必需的：用户来投诉「我为什么被拦」时，没有命中记录就无法解释；
// 它也是判断「规则是否误伤」的唯一依据。
//
// HitCount 是**聚合列**：同一 (EntryID, ClientIP) 在 1 分钟窗口内只写一条行，
// 后续命中累加到 HitCount（写库路径见 service/blacklist_cache.go 的聚合器）。
// 没有这一列，被刷 1000 次就是 1000 行。
type BlacklistHit struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// EntryID 命中哪条规则；0 表示「不知道命中哪条」（fail-open 留痕行）。
	EntryID int64  `gorm:"column:entry_id;not null;default:0;index:idx_blacklist_hit_entry,priority:1" json:"entryId"`
	Kind    string `gorm:"column:kind;type:varchar(16);not null;default:''" json:"kind"`
	// ValueMasked 是命中的请求值（脱敏），不是规则原文。
	ValueMasked string `gorm:"column:value_masked;type:varchar(191);not null;default:''" json:"valueMasked"`
	ClientIP    string `gorm:"column:client_ip;type:varchar(64);not null;default:''" json:"clientIp"`
	// Path 是请求路径（截断到 255 字符，按字符而不是字节，见 AGENTS.md 第 1 条）。
	Path string `gorm:"column:path;type:varchar(255);not null;default:''" json:"path"`
	// Action 取 blocked / logged_only（logged_only = fail-open 时只留痕不拦）。
	Action string `gorm:"column:action;type:varchar(16);not null;default:''" json:"action"`
	// HitCount 是聚合窗口内的累计命中次数（首次写入为 1）。
	HitCount  int64     `gorm:"column:hit_count;not null;default:1" json:"hitCount"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime;index:idx_blacklist_hit_entry,priority:2;index:idx_blacklist_hit_created,priority:1" json:"createdAt"`
}

func (*BlacklistHit) TableName() string { return TableNameBlacklistHit }
