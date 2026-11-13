package entity

import "time"

// TableNameTicketAttachment 是工单图片附件表。
//
// 它与 ticket_refund_proofs **刻意分开**：退款凭证是「人工退款审核前必须核验」的
// 受限单据（见 entity.TicketRefundProof 的注释），一个工单可以有多张普通截图，
// 两者生命周期、权限与审计要求都不同，不能共用一张表或一套读取接口。
//
// 安全方案照抄退款凭证：明文只以 AES-GCM 密文 + nonce 落库，解密后仍需通过
// 展示判定（scan_state）才允许下发字节。密文列 `json:"-"`，永远不进入接口响应。
type TicketAttachment struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// TicketID 是附件所属工单；查询、鉴权与删除都以它为准。
	TicketID int64 `gorm:"column:ticket_id;not null;index" json:"ticketId"`
	// MessageID 可空：为空表示「挂在工单上」的图片（发工单时带图），
	// 非空表示挂在某条回复消息下（回复时带图）。
	MessageID *int64 `gorm:"column:message_id;index" json:"messageId,omitempty"`
	// UserID 是上传者，必须是工单参与者；用户端读取时以它 + 工单归属判权。
	UserID    int32  `gorm:"column:user_id;not null;index" json:"userId"`
	MediaType string `gorm:"column:media_type;type:varchar(32);not null" json:"mediaType"`
	ByteSize  int64  `gorm:"column:byte_size;not null" json:"byteSize"`
	SHA256    string `gorm:"column:sha256;type:varchar(64);not null" json:"sha256"`

	// 密文与 IV（GCM nonce）。列上带 json:"-"，明文与密文都不出接口。
	//
	// 两列**故意可空**：AAD 含自增 id，所以加密必须发生在 INSERT 之后（先拿 id 再加密），
	// 用法是「同一事务内先插入占位行、再 UPDATE 写入密文」。事务提交后这两列必有值；
	// 读取路径对 len(nonce)/len(ciphertext) 的检查会拒绝任何残缺行。
	Nonce      []byte `gorm:"column:nonce;type:blob" json:"-"`
	Ciphertext []byte `gorm:"column:ciphertext;type:longblob" json:"-"`

	// ScanState 是**唯一**的展示判据（fail-closed）：
	// pending / scanning / sensitive / failed 一律不显示，只有 approved 才显示。
	ScanState string `gorm:"column:scan_state;type:varchar(16);not null;index" json:"scanState"`
	// 敏感检测结果。分数用指针，便于区分「未检测」与「检测为 0」。
	NSFWScore    *float64   `gorm:"column:nsfw_score" json:"nsfwScore,omitempty"`
	RiskLevel    string     `gorm:"column:risk_level;type:varchar(16)" json:"riskLevel,omitempty"`
	Suggestion   string     `gorm:"column:suggestion;type:varchar(16)" json:"suggestion,omitempty"`
	Label        string     `gorm:"column:label;type:varchar(32)" json:"label,omitempty"`
	CheckError   string     `gorm:"column:check_error;type:text" json:"-"`
	CheckedAt    *time.Time `gorm:"column:checked_at" json:"checkedAt,omitempty"`
	AttemptCount int        `gorm:"column:attempt_count;not null;default:0" json:"-"`
	// NextAttemptAt 与 ticket_notifications 的重试列同构：失败的检测按退避重排。
	NextAttemptAt *time.Time `gorm:"column:next_attempt_at;index" json:"-"`

	// PurgedAt 非空表示这张图片的**密文已按保留策略清理**（service 包的
	// ticket_attachment_retention.go），值即清理时刻。
	//
	// # 为什么是「留行置空」而不是物理删行
	//
	// 这一行同时是「某工单/某条回复上的某张图」这条业务关系，message_id 是唯一绑定。
	// 物理删行会让历史消息出现无法解释的空洞（前端拿到 n 条消息却只有 n-1 条带图，
	// 且无法区分「当时没发图」与「图被清理了」）。所以清理只做内容降级：
	// ciphertext / nonce 置空，byte_size / media_type / sha256 / message_id 原样保留，
	// 前端据此显示「图片已按保留策略清理」。
	//
	// # 三态语义（读取路径不要混为一谈）
	//
	//	nil        → 正常图片
	//	非 nil     → 已按策略清理（可解释的占位，不是数据损坏）
	//	密文为空且 nil → **残缺行**（加密事务崩在中间），不得当成已清理
	//
	// 因此判「是否已清理」只能看本列，不要用「ciphertext 为空」推断。
	PurgedAt *time.Time `gorm:"column:purged_at" json:"purgedAt,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime;index" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*TicketAttachment) TableName() string { return TableNameTicketAttachment }
