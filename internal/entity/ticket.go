package entity

import "time"

const (
	TableNameTicket             = "tickets"
	TableNameTicketMessage      = "ticket_messages"
	TableNameTicketNotification = "ticket_notifications"
)

const (
	TicketStatusOpen     = "open"
	TicketStatusPending  = "pending"
	TicketStatusResolved = "resolved"
	TicketStatusClosed   = "closed"
	TicketPriorityNormal = "normal"
)

type Ticket struct {
	ID            int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        int32      `gorm:"column:user_id;not null;index" json:"userId"`
	UserName      string     `gorm:"column:user_name;type:varchar(255);not null" json:"userName"`
	Subject       string     `gorm:"column:subject;type:varchar(120);not null" json:"subject"`
	Status        string     `gorm:"column:status;type:varchar(32);not null;index" json:"status"`
	Priority      string     `gorm:"column:priority;type:varchar(32);not null;default:normal" json:"priority"`
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
