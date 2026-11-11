package entity

import "time"

const (
	TableNameTicket             = "tickets"
	TableNameTicketMessage      = "ticket_messages"
	TableNameTicketNotification = "ticket_notifications"
	TableNameTicketAttachment   = "ticket_attachments"
)

// 工单图片附件的检测状态。ScanState 是展示判据：只有 approved 显示图片，
// 其余（含 pending / failed）一律按敏感处理，见 service 包的 fail-closed 判定。
const (
	TicketAttachmentScanPending   = "pending"
	TicketAttachmentScanScanning  = "scanning"
	TicketAttachmentScanApproved  = "approved"
	TicketAttachmentScanSensitive = "sensitive"
	// TicketAttachmentScanFailed 表示「检测没能得出结论」（无密钥 / 超时 / 上游错误
	// 且重试耗尽）。名字刻意不是 rejected：它不是「判为正常」，而是「不知道」，
	// 对外表现与 sensitive 相同（不显示），但保留独立状态便于运维区分。
	TicketAttachmentScanFailed = "failed"
)

const (
	TicketStatusOpen     = "open"
	TicketStatusPending  = "pending"
	TicketStatusResolved = "resolved"
	TicketStatusClosed   = "closed"
)

// 工单三个正交维度之一：origin（发起方）—— 决定「谁能看、谁来处理」。
//
// 不要用一个 type 字段承载全部差异：origin 管可见性与处理人，category 管表单、
// 校验与专属动作，priority 管等待时长。三者互相独立，合并后必然是 switch 地狱。
const (
	// TicketOriginUser 用户主动发起（唯一允许用户自选入口的来源）。
	TicketOriginUser = "user"
	// TicketOriginSystem 系统自动建单（如 Q26 注册赠金判定失败）。
	// 被涉及用户**可见**（45-*.md §0.23.2 Q69-2 推翻「保密」原建议）：
	// 隔离只靠 UserID，不靠 origin；用户可回复，但**不能改** category / priority。
	TicketOriginSystem = "system"
	// TicketOriginAppeal 申诉（封禁申诉 Q-B1 / Q-B4），申诉人自己可见。
	TicketOriginAppeal = "appeal"
)

// 维度之二：category（业务分类）—— 决定表单、校验与专属动作。
//
// ⚠️ category **不得由用户自选**：建单时由服务端按来源强制赋值。
// 否则用户可以挑「能一键补发赠金」的类别套利（69-*.md §2.2 / §七.1）。
const (
	TicketCategoryGeneral           = "general"
	TicketCategoryRegistrationBonus = "registration_bonus"
	TicketCategoryBlacklistAppeal   = "blacklist_appeal"
	TicketCategoryAccountRebind     = "account_rebind"
	TicketCategoryAccountClosure    = "account_closure"
	TicketCategoryRefund            = "refund"
)

// 维度之三：priority（优先级）—— 四档，urgent 仅管理员可设。
const (
	TicketPriorityLow    = "low"
	TicketPriorityNormal = "normal"
	TicketPriorityHigh   = "high"
	TicketPriorityUrgent = "urgent"
)

// ticket_messages.author_role 的取值；system 由 CreateSystemTicketWithDB 写入。
const (
	TicketAuthorRoleUser   = "user"
	TicketAuthorRoleAdmin  = "admin"
	TicketAuthorRoleSystem = "system"
)

type Ticket struct {
	ID       int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID   int32  `gorm:"column:user_id;not null;index" json:"userId"`
	UserName string `gorm:"column:user_name;type:varchar(255);not null" json:"userName"`
	Subject  string `gorm:"column:subject;type:varchar(120);not null" json:"subject"`
	Status   string `gorm:"column:status;type:varchar(32);not null;index" json:"status"`
	Priority string `gorm:"column:priority;type:varchar(32);not null;default:normal" json:"priority"`
	// Origin / Category / Context 是 69-*.md 的多类型三维度落点。
	// default 只作用于**插入**，已有行会落空串 —— 必须靠
	// registry.ensureTicketTypeSchema 的存量回填补 origin='user'，
	// 否则按 origin 过滤会漏掉全部旧工单。
	Origin   string `gorm:"column:origin;type:varchar(16);not null;default:user;index" json:"origin"`
	Category string `gorm:"column:category;type:varchar(32);not null;default:general;index" json:"category"`
	// Context 是**建单那一刻**的类别上下文快照（JSON）：当时的命中规则、当时的失败原因。
	// 硬约束：不得存凭据 —— 手机号只留后四位，验证码 / token / password 一律抹掉
	// （service.marshalTicketContext 落库前过一道 redact）。只做原样展示，不按字段查询。
	Context       string     `gorm:"column:context;type:text" json:"context,omitempty"`
	LastMessageAt time.Time  `gorm:"column:last_message_at;not null;index" json:"lastMessageAt"`
	ClosedAt      *time.Time `gorm:"column:closed_at" json:"closedAt,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime;index" json:"createdAt"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*Ticket) TableName() string { return TableNameTicket }

type TicketMessage struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TicketID   int64     `gorm:"column:ticket_id;not null;index" json:"ticketId"`
	UserID     int32     `gorm:"column:user_id;not null;index" json:"userId"`
	AuthorName string    `gorm:"column:author_name;type:varchar(255);not null" json:"authorName"`
	Body       string    `gorm:"column:body;type:text;not null" json:"body"`
	AuthorRole string    `gorm:"column:author_role;type:varchar(16);not null;index" json:"authorRole"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime;index" json:"createdAt"`
}

func (*TicketMessage) TableName() string { return TableNameTicketMessage }

type TicketNotification struct {
	ID            int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	TicketID      int64      `gorm:"column:ticket_id;not null;index" json:"ticketId"`
	EventType     string     `gorm:"column:event_type;type:varchar(32);not null" json:"eventType"`
	Payload       string     `gorm:"column:payload;type:text;not null" json:"-"`
	MentionedList string     `gorm:"column:mentioned_list;type:text" json:"-"`
	Status        string     `gorm:"column:status;type:varchar(16);not null;index" json:"status"`
	AttemptCount  int        `gorm:"column:attempt_count;not null;default:0" json:"attemptCount"`
	LastError     string     `gorm:"column:last_error;type:text" json:"lastError,omitempty"`
	NextAttemptAt *time.Time `gorm:"column:next_attempt_at;index" json:"nextAttemptAt,omitempty"`
	SentAt        *time.Time `gorm:"column:sent_at" json:"sentAt,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime;index" json:"createdAt"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*TicketNotification) TableName() string { return TableNameTicketNotification }
