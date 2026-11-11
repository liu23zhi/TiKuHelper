package entity

import "time"

const TableNameIPSegment = "ip_segments"

// IPSegment 是一条 IP 段归属记录（task_doc/71）。
//
// # 为什么新建这张表，而不是改造 location_cache_entries
//
// `location_cache_entries` 的主键是 `(kind, lookup_key, source)`，语义是
// 「**查过这个键**」；本表的语义是「**这个网段属于谁**」。两件不同的事：
//   - 缓存表保证「同一个 IP 不重复查询」；
//   - 段表保证「**同一段内不重复查询**」—— 上游一次返回 beginip/endip，
//     说明一次查询就能覆盖一整段，段内后续 IP 全部零调用命中。
//
// 上游样例（standard）：
//
//	{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","llc":"GOOGLE",
//	 "asn":"AS15169","latitude":37.751,"longitude":-97.822,
//	 "beginip":"8.8.8.0","endip":"8.8.8.255"}
//
// # 两个关键设计决定
//
//  1. **BeginIP / EndIP 用 uint32 数值存储，不是点分十进制字符串。**
//     区间查询是 `WHERE begin_ip <= ? AND end_ip >= ?`，必须数值比较。
//     存字符串会按字典序比较 —— `'9.0.0.0' > '10.0.0.0'`，结果全错。
//     BeginText / EndText 保留原始文本，供展示与「数值与文本对不上时」对照排查。
//
//  2. **Latitude / Longitude 用 *float64，不用 float64。**
//     上游在部分响应里可能缺这两个字段。用 0 会被误认为「赤道/本初子午线上的
//     真实坐标」——(0,0) 是几内亚湾的真实位置，不是「没有数据」。
type IPSegment struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// BeginIP / EndIP 是网段起止地址的数值形式（IPv4 专用）。
	//
	// ⚠️ IPv6 不进本表：128 位存不进 uint32。IPv6 的响应通常也不含
	// beginip/endip —— 见 service 层的解析守卫。
	//
	// 与 Source 组成复合唯一索引 idx_ip_segment_unique：同一段同一来源只有一行，
	// 这是 Upsert（ON CONFLICT）能工作的前提 —— 没有唯一约束时
	// SQLite 会直接报 "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint"。
	BeginIP uint32 `gorm:"column:begin_ip;not null;index:idx_ip_segment_range,priority:1;uniqueIndex:idx_ip_segment_unique,priority:1" json:"beginIp"`
	EndIP   uint32 `gorm:"column:end_ip;not null;index:idx_ip_segment_range,priority:2;uniqueIndex:idx_ip_segment_unique,priority:2" json:"endIp"`
	// BeginText / EndText 是上游原始的点分十进制写法（含可能的非规范形式）。
	BeginText string `gorm:"column:begin_text;type:varchar(45);not null" json:"beginText"`
	EndText   string `gorm:"column:end_text;type:varchar(45);not null" json:"endText"`
	// SampleIP 是产生这条记录的查询 IP，用于反查「这条记录是怎么来的」。
	SampleIP string `gorm:"column:sample_ip;type:varchar(45);not null" json:"sampleIp"`
	// Region 形如「中国 广东 中山」/「美国」/「中国 北京」，段数不固定。
	Region string `gorm:"column:region;type:varchar(255)" json:"region"`
	// ISP 是运营商全名（如 China Mobile Communications Group Co., Ltd.）。
	ISP string `gorm:"column:isp;type:varchar(255)" json:"isp"`
	// LLC 是归属机构简称（如「移动」「GOOGLE」），黑名单地理匹配优先用它。
	LLC string `gorm:"column:llc;type:varchar(64)" json:"llc"`
	// ASN 形如 AS9808 / AS15169。
	ASN string `gorm:"column:asn;type:varchar(32);index" json:"asn"`
	// Latitude / Longitude：nil = 上游没给，不是 0。
	Latitude  *float64 `gorm:"column:latitude" json:"latitude,omitempty"`
	Longitude *float64 `gorm:"column:longitude" json:"longitude,omitempty"`
	// Source 是查询模式（standard / commercial），与 location_cache_entries 同义。
	// 同一段可能因 source 不同给出不同精度，各自保留一份。
	Source string `gorm:"column:source;type:varchar(32);not null;default:'';index:idx_ip_segment_range,priority:3;uniqueIndex:idx_ip_segment_unique,priority:3" json:"source"`
	// RawJSON 保留上游原始响应 —— 上游字段有增删历史，结构化列不全时
	// 它是唯一还原手段（与 location_cache_entries.raw_json 同一理由）。
	RawJSON   string    `gorm:"column:raw_json;type:text" json:"rawJson"`
	FetchedAt time.Time `gorm:"column:fetched_at;not null;index" json:"fetchedAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*IPSegment) TableName() string {
	return TableNameIPSegment
}
