package entity

import "time"

// RefundPayoutQrMemory is the current remembered image. A zero attachment ID is
// a durable deletion marker, preventing fallback to a previously submitted QR.
type RefundPayoutQrMemory struct {
	UserID       int32     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	AttachmentID int64     `gorm:"not null;default:0;index" json:"-"`
	Version      string    `gorm:"type:varchar(64);not null" json:"-"`
	CreatedAt    time.Time `json:"-"`
	UpdatedAt    time.Time `json:"-"`
}

// Each uploaded image keeps its original public key permanently. Remembering a
// different image must never let an old image be claimed with a new device key.
type RefundPayoutQrBinding struct {
	AttachmentID int64     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	UserID       int32     `gorm:"not null;index" json:"-"`
	KeyID        string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	PublicKey    []byte    `gorm:"type:blob;not null" json:"-"`
	CreatedAt    time.Time `json:"-"`
}

type RefundPayoutQrChallenge struct {
	ID            string     `gorm:"primaryKey;type:varchar(64)" json:"-"`
	UserID        int32      `gorm:"not null;index" json:"-"`
	AttachmentID  int64      `gorm:"not null" json:"-"`
	MemoryVersion string     `gorm:"type:varchar(64);not null" json:"-"`
	KeyID         string     `gorm:"type:varchar(64);not null" json:"-"`
	Challenge     []byte     `gorm:"type:blob;not null" json:"-"`
	ExpiresAt     time.Time  `gorm:"not null;index" json:"-"`
	UsedAt        *time.Time `json:"-"`
	CreatedAt     time.Time  `json:"-"`
}

func (*RefundPayoutQrMemory) TableName() string    { return "refund_payout_qr_memories" }
func (*RefundPayoutQrBinding) TableName() string   { return "refund_payout_qr_bindings" }
func (*RefundPayoutQrChallenge) TableName() string { return "refund_payout_qr_challenges" }
