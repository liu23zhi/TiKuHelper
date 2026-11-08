package entity

import "time"

// TicketRefundProof is not an ordinary ticket attachment. Its plaintext,
// storage identifiers, and original filename must not reach lists or webhooks.
type TicketRefundProof struct {
	RefundID   int64     `gorm:"column:refund_id;primaryKey;autoIncrement:false" json:"-"`
	UserID     int32     `gorm:"column:user_id;not null" json:"-"`
	MediaType  string    `gorm:"column:media_type;type:varchar(32);not null" json:"-"`
	Nonce      []byte    `gorm:"column:nonce;type:blob;not null" json:"-"`
	Ciphertext []byte    `gorm:"column:ciphertext;type:longblob;not null" json:"-"`
	CreatedAt  time.Time `json:"-"`
}

func (*TicketRefundProof) TableName() string { return "ticket_refund_proofs" }
