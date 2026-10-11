package entity

import "time"

const TableNameLocationCache = "location_cache"

// LocationCache 是归属地查询结果的**持久化**缓存。
//
// 为什么落库而不是只放内存：
//   - UApiPro 的查询按积分计费（手机 2 分/次、IP 1 分/次），进程重启后内存缓存全丢，
//     用户管理/日志列表每翻一页都要重新计费；
//   - 多实例部署时内存缓存各自一份，命中率按实例打折；
//   - 归属地是"低频变化"数据，落库后可以给到远长于进程生命周期的 TTL。
//
// 一行 = 一次成功的归属地查询结果。
type LocationCache struct {
	// Kind 区分查询类型：phone 或 ip。
	Kind string `gorm:"column:kind;type:varchar(16);not null;primaryKey" json:"kind"`
	// LookupKey 是归一化后的查询键：手机号（11 位数字）或 IP/域名原文。
	LookupKey string `gorm:"column:lookup_key;type:varchar(255);not null;primaryKey" json:"lookupKey"`
	// Address 是用于展示的归属地文本，例如「云南玉溪 移动」或「美国」。
	Address string `gorm:"column:address;type:varchar(255)" json:"address"`
	// RawJSON 保留上游返回的原始响应。
	//
	// 官方文档未列出 source=commercial 时"更完整的地理位置信息"的具体字段名，
	// 因此必须原样保存，避免以后想用这些字段时才发现数据已经丢了。
	RawJSON string `gorm:"column:raw_json;type:text" json:"rawJson"`
	// FetchedAt 是写入时刻，用于判断是否过期（TTL 在服务层按 kind 决定）。
	FetchedAt time.Time `gorm:"column:fetched_at;not null;index" json:"fetchedAt"`
	// UpdatedAt 便于排查"这条记录最后一次刷新是什么时候"。
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

// TableName LocationCache's table name
func (*LocationCache) TableName() string {
	return TableNameLocationCache
}

// 归属地查询类型。
const (
	LocationKindPhone = "phone"
	LocationKindIP    = "ip"
)
