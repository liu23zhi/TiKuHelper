package entity

import "time"

const TableNameLocationCache = "location_cache_entries"
const TableNameLegacyLocationCache = "location_cache"

// LocationCache 是归属地查询结果的**持久化**缓存。
//
// 为什么落库而不是只放内存：
//   - UApiPro 的查询按积分计费（手机 2 分/次、IP 1 分/次），进程重启后内存缓存全丢，
//     用户管理/日志列表每翻一页都要重新计费；
//   - 多实例部署时内存缓存各自一份，命中率按实例打折；
//   - 归属地是"低频变化"数据，落库后可以给到远长于进程生命周期的 TTL。
//
// 一行 = 一次成功的归属地查询结果。有效地址默认永久复用，管理员显式刷新时更新。
type LocationCache struct {
	// Kind 区分查询类型：phone 或 ip。
	Kind string `gorm:"column:kind;type:varchar(16);not null;primaryKey" json:"kind"`
	// LookupKey 是归一化后的查询键：手机号（11 位数字）或 IP/域名原文。
	LookupKey string `gorm:"column:lookup_key;type:varchar(255);not null;primaryKey" json:"lookupKey"`
	// Source 区分 IP 查询模式；手机号固定为空。历史来源未知时使用 LocationSourceLegacy。
	Source string `gorm:"column:source;type:varchar(32);not null;default:'';primaryKey" json:"source"`
	// Address 是用于展示的归属地文本，例如「云南玉溪 移动」或「美国」。
	Address string `gorm:"column:address;type:varchar(255)" json:"address"`
	// RawJSON 保留上游返回的原始响应。
	//
	// 官方文档未列出 source=commercial 时"更完整的地理位置信息"的具体字段名，
	// 因此必须原样保存，避免以后想用这些字段时才发现数据已经丢了。
	RawJSON string `gorm:"column:raw_json;type:text" json:"rawJson"`
	// FetchedAt 是最近一次查询成功的时间，也用于管理列表排序。
	FetchedAt time.Time `gorm:"column:fetched_at;not null;index" json:"fetchedAt"`
	// UpdatedAt 便于排查"这条记录最后一次刷新是什么时候"。
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

// TableName LocationCache's table name
func (*LocationCache) TableName() string {
	return TableNameLocationCache
}

// LegacyLocationCache 映射迁移前的旧表。旧表没有 source，读取只用于一次性回填新表。
type LegacyLocationCache struct {
	Kind      string    `gorm:"column:kind"`
	LookupKey string    `gorm:"column:lookup_key"`
	Address   string    `gorm:"column:address"`
	RawJSON   string    `gorm:"column:raw_json"`
	FetchedAt time.Time `gorm:"column:fetched_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (*LegacyLocationCache) TableName() string {
	return TableNameLegacyLocationCache
}

// 归属地查询类型。
const (
	LocationKindPhone = "phone"
	LocationKindIP    = "ip"
	// LocationSourceLegacy 只保留旧记录，不作为上游查询模式使用。
	LocationSourceLegacy = "legacy"
)
