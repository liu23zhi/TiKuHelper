package entity

import "time"

// 表名：敏感图片检测缓存 + 每人每日额度（117-*.md §5.1 / 121-*.md §4.1、§4.2）。
const (
	TableNameSensitiveImageCache = "sensitive_image_cache_entries"
	TableNameSensitiveImageQuota = "sensitive_image_daily_quotas"
)

// 缓存状态四态（109-*.md §2.3）。
//
// pending 的存在理由只有一个：**防并发重复外呼**。两个用户同时上传同一张图时，
// 先到者写 pending 并开始外呼，后到者看到 pending 就等待（或直接返回「检测中」），
// 而不是再调一次计费接口。超时未更新的 pending 视为失效（见 service 侧的抢占超时）。
const (
	SensitiveImageCacheStatusPending   = "pending"
	SensitiveImageCacheStatusClean     = "clean"
	SensitiveImageCacheStatusSensitive = "sensitive"
	// SensitiveImageCacheStatusError 表示上一次外呼没跑成。它**不是终态**：
	// 下次上传同一张图时会允许抢占重试，否则一次上游抖动会把这张图永久钉死。
	SensitiveImageCacheStatusError = "error"
)

// SensitiveImageCacheEntry 缓存敏感图片检测结果，按**图片内容**去重。
//
// 与 ai_ocr_cache_entries 的三处差异（其余结构刻意镜像，便于运维共用一套心智）：
//  1. 主键是 ImageSHA256 而不是 URLHash —— 工单图片是上传的，没有稳定 URL；
//     同一张图被不同工单上传时必须命中同一条缓存（117-*.md N1-b「检测跟 blob 走」）。
//  2. 没有 URL 字段 —— 上传的图片没有可访问地址。
//  3. 有 pending 状态 —— 敏感检测涉及计费外呼，并发时必须靠占位避免重复调用。
//
// ⚠️ 本表**不保存图片字节**。字节在 ticket_attachments 里（AES-GCM 密文），
// 缓存只记「这个 SHA256 的判定结论」，所以它天然是一张小的、可随时清空的表。
type SensitiveImageCacheEntry struct {
	// ID 镜像 ai_ocr_cache_entries 的展示编号列。本版本不分配（留 NULL），
	// 保留它只为让「以后要稳定的展示 ID」不必再加一次迁移。
	ID *uint64 `gorm:"column:id;uniqueIndex" json:"id"`

	// ImageSHA256 是 sha256(图片二进制) 的十六进制串，char(64) 主键。
	//
	// 主键就是去重键：同一个 SHA256 在全世界只有一行，因此「同一张图不同工单只检测一次」
	// 是**结构保证**，不依赖任何业务代码判断。
	ImageSHA256 string `gorm:"column:image_sha256;type:char(64);primaryKey" json:"imageSha256"`

	ContentType string `gorm:"column:content_type;type:varchar(128);not null;default:''" json:"contentType"`
	FileSize    int64  `gorm:"column:file_size;type:bigint;not null;default:0" json:"fileSize"`

	// Status 四态：pending / clean / sensitive / error（见上方常量）。
	Status string `gorm:"column:status;type:varchar(32);not null;default:'pending';index" json:"status"`
	// Reason 是可解释的判定原因（如「上游判定为敏感图片」「quota_exceeded」）。
	// 不得放上游原始响应体：它可能含模型细节，且对排障没有额外价值。
	Reason string `gorm:"column:reason;type:varchar(255);not null;default:''" json:"reason"`
	// Provider 记录是谁判定的：uapi / manual（管理员手动触发）。
	// 手动触发会把 pending→结果 的 pending 也覆盖过来，于是这一列能区分
	// 「机器判过一次又被人复核」与「机器本来就这么判」。
	Provider string `gorm:"column:provider;type:varchar(64);not null;default:''" json:"provider"`

	// HitCount 是被命中次数，用于统计「重复上传」的量级。
	// 注意它**不是**额度计数：额度在 sensitive_image_daily_quotas 里按用户+自然日算。
	HitCount int32 `gorm:"column:hit_count;type:int;not null;default:0" json:"hitCount"`

	FirstSeenAt time.Time `gorm:"column:first_seen_at;autoCreateTime" json:"firstSeenAt"`
	// UpdatedAt 同时承担两个语义：最后一次结果写入时间，以及**pending 占位的存活时钟**
	// （超时判定与抢占都基于它）。因此写结果与写占位都必须显式推进它。
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime;index" json:"updatedAt"`
}

func (*SensitiveImageCacheEntry) TableName() string { return TableNameSensitiveImageCache }

// SensitiveImageDailyQuota 是每人每日的敏感检测计数。
//
// 为什么用独立表而不是从缓存表聚合（109-*.md §2.4）：额度检查在**每次上传**时
// 都要做，COUNT 聚合会随数据量线性变慢；独立表可以用复合主键 + 单行条件更新，
// 「查 + 增」一条语句完成，天然并发安全。
//
// 口径（111-*.md Q1-g 及后续修订）：
//   - 缓存**未超额**时命中 → 计 1 次（额度限制的是上传次数，不是外呼次数）；
//   - 额度**已耗尽**时缓存命中 → 不计（直接返回缓存结论）；
//   - 额度已耗尽且缓存未命中 → 不计、不外呼，标记 sensitive_by_quota。
type SensitiveImageDailyQuota struct {
	// UserID 语义上指向 users.id。本表与 users 之间**不建外键约束**（与仓内其它表一致），
	// 但迁移有顺序依赖：users 必须先存在（121-*.md §4.2）。
	UserID int32 `gorm:"column:user_id;type:int(11);primaryKey;autoIncrement:false" json:"userId"`
	// QuotaDate 是**自然日**（Asia/Shanghai），格式 YYYY-MM-DD。
	//
	// 用字符串而不是 DATE 类型：SQLite 没有真正的日期类型，用 char(10) 让两种方言
	// 的存储与比较行为完全一致（字典序即时间序），也避免时区在驱动层被二次解释。
	QuotaDate string `gorm:"column:quota_date;type:char(10);primaryKey" json:"quotaDate"`
	// UsedCount 已消耗额度，只增不减（不做发放/回收，跨日靠换日期键自然归零）。
	UsedCount int32     `gorm:"column:used_count;type:int;not null;default:0" json:"usedCount"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*SensitiveImageDailyQuota) TableName() string { return TableNameSensitiveImageQuota }
