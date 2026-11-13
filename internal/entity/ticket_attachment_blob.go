package entity

import "time"

// TableNameTicketAttachmentBlob 是工单图片**二进制本体**表（task_doc/112 N1）。
//
// 与 ticket_attachments 的分工是「关系」与「内容」的分工：
//   - ticket_attachments 一行 = 「某工单（或某条回复）上的某张图」这个业务关系；
//   - ticket_attachment_blobs 一行 = 一份图片字节，按 sha256(明文) 去重。
//
// 为什么必须拆开：同一张图会被不同工单、不同用户、不同时间反复上传。
// 把字节留在关系行里，100 次上传就是 100 份完全相同的密文（每份可达 20 MB）。
// 拆开之后 100 次上传只有 1 份字节 + 100 行关系。
const TableNameTicketAttachmentBlob = "ticket_attachment_blobs"

// TicketAttachmentBlob 是图片二进制的唯一存储。同一张图（同 SHA256）只存一行。
//
// # 主键为什么是 SHA256 而不是自增 ID
//
// 去重的判据就是「内容相同」，用内容指纹做主键，唯一约束天然表达去重语义：
// 并发插入同一张图时数据库直接挡住第二个（配合 ON CONFLICT DO NOTHING 变成
// 「引用计数 +1」），不需要先查后插的竞态窗口。
//
// # 代价：加密 AAD 必须换
//
// 旧表 ticket_attachments 的 AAD 含自增 id（见该文件 :28 注释），
// 这张表没有自增 id，所以 AAD 换成 sha256，见 service.TicketAttachmentBlobAAD。
//
// # 密文列不出接口
//
// Nonce / Ciphertext / CheckError 都带 `json:"-"`：它们的唯一出口是
// service 层的解密 + 展示判定（scan_state），不允许被任何 handler 直接序列化。
type TicketAttachmentBlob struct {
	// SHA256 是 sha256(明文图片二进制) 的小写十六进制，char(64) 主键 —— 去重的唯一依据。
	SHA256 string `gorm:"column:sha256;type:char(64);primaryKey" json:"sha256"`

	ByteSize  int64  `gorm:"column:byte_size;not null" json:"byteSize"`
	MediaType string `gorm:"column:media_type;type:varchar(32);not null" json:"mediaType"`

	// 密文与 nonce。AAD 用 SHA256 而不是自增 ID —— 这张表没有自增 ID，
	// 且 SHA256 本身就是稳定的主键。
	Nonce      []byte `gorm:"column:nonce;type:blob" json:"-"`
	Ciphertext []byte `gorm:"column:ciphertext;type:longblob" json:"-"`

	// RefCount 是引用计数：有多少个 ticket_attachments 行指向这份 blob。
	//
	// ⚠️ 硬约束（task_doc/121 §4.4 第 1 点）：它必须**恒等于**引用行数。
	// 少算就会让 24 小时延迟删除删掉还在被引用的 blob（用户看到图片永久 404），
	// 所以计数只在「同一事务内重算」这一个地方产生，不做增量猜测。
	RefCount int32 `gorm:"column:ref_count;not null;default:0;index" json:"refCount"`

	// 敏感检测结果**跟着 blob 走**，不是跟着附件行走（112 N1-b）：
	// 同一张图检测一次就够，不必每个工单重检（省外呼费用）。
	ScanState string `gorm:"column:scan_state;type:varchar(16);not null;index" json:"scanState"`

	NSFWScore     *float64   `gorm:"column:nsfw_score" json:"nsfwScore,omitempty"`
	RiskLevel     string     `gorm:"column:risk_level;type:varchar(16)" json:"riskLevel,omitempty"`
	Suggestion    string     `gorm:"column:suggestion;type:varchar(16)" json:"suggestion,omitempty"`
	Label         string     `gorm:"column:label;type:varchar(32)" json:"label,omitempty"`
	CheckError    string     `gorm:"column:check_error;type:text" json:"-"`
	CheckedAt     *time.Time `gorm:"column:checked_at" json:"checkedAt,omitempty"`
	AttemptCount  int        `gorm:"column:attempt_count;not null;default:0" json:"-"`
	NextAttemptAt *time.Time `gorm:"column:next_attempt_at;index" json:"-"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	// UpdatedAt 有第二重用途：延迟删除按它判龄（ref_count<=0 且超过 24 小时）。
	// 因此**引用计数归零时必须刷新它**（见 service.ReleaseTicketAttachmentBlobInTx），
	// 否则「删了又传」会把缓冲期算错。
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*TicketAttachmentBlob) TableName() string { return TableNameTicketAttachmentBlob }

// TicketAttachmentBlobLink 是 ticket_attachments 表上 blob_sha256 列的**非生成**声明。
//
// # 为什么不直接往 entity.TicketAttachment 里加字段
//
// 与 UserArchive / LogSensitive 同一条纪律：新列由显式 ensure*Schema 迁移补，
// 而 `Migrator().AddColumn` 需要一个带该字段的模型。把字段加进既有实体，会让
// AutoMigrate 在每次启动时试图改动已有表（本仓明确禁止）。
//
// 因此把这一列声明成同一张物理表的第二个结构体，只服务于：
//   - MySQL/MariaDB 的 `Migrator().AddColumn(&entity.TicketAttachmentBlobLink{}, "BlobSHA256")`；
//   - 回填时的 UPDATE。
//
// ⚠️ 纪律：**不要**对它调用 AutoMigrate，也不要把它和 TicketAttachment 传进
// **同一次** AutoMigrate 调用 —— GORM 在那种调用里会把两个模型合并成一条
// CREATE TABLE，结果表只剩 `id` + `blob_sha256` 两列（实测复现）。
// 它缺少 ticket_attachments 其余列，AutoMigrate 会试图改动已存在的表结构。
//
// ⚠️ 列定义为什么带 `default:` 加空串：给**已有行**加 NOT NULL 列时，MySQL 严格模式
// 与 SQLite 都要求一个可用的默认值；空串同时充当「还没回填」的哨兵，
// 回填判据就是「blob_sha256 的 COALESCE 结果等于空串」。
//
// ⚠️ 旧列 nonce / ciphertext 一个版本内不删（112 N1-c）：这张表加列是**纯新增**，
// 回滚时旧代码照旧读旧列即可，不需要改任何现成文件。
type TicketAttachmentBlobLink struct {
	// ID 映射 ticket_attachments.id，仅作为 Where 的锚点。
	ID int64 `gorm:"column:id;primaryKey" json:"id"`
	// BlobSHA256 指向 ticket_attachment_blobs.sha256。
	//
	// 为什么保留这个自有列而不是靠 sha256 现算：删附件时要在**同一事务**里
	// 原子地递减 RefCount，需要一列直接可读的指向。
	BlobSHA256 string `gorm:"column:blob_sha256;type:char(64);not null;default:'';index" json:"blobSha256"`
}

// TableName 与 entity.TicketAttachment 指向同一张物理表。
func (*TicketAttachmentBlobLink) TableName() string { return TableNameTicketAttachment }

// TicketAttachmentBlobDedupGrace 是延迟删除的缓冲期（112 N1-a / 117 §4.1）。
//
// 归零后要等 24 小时才真删：用户「删了又传」的高频场景下，立即删除会让同一份
// 20 MB 密文被反复写盘。24 小时是缓冲期，不是业务时效。
const TicketAttachmentBlobDedupGrace = 24 * time.Hour
