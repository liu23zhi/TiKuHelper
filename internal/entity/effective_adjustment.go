package entity

import "time"

// EffectiveAdjustment only changes the recognition of an existing source. It
// never updates wallet movements, gateway outcomes, or the source itself.
type EffectiveAdjustment struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	UserID     int32  `gorm:"not null;index" json:"userId"`
	SourceKind string `gorm:"type:varchar(12);not null" json:"sourceKind"`
	SourceID   int32  `gorm:"not null" json:"sourceId"`
	OperatorID int32  `gorm:"not null" json:"operatorId"`
	Reason     string `gorm:"type:varchar(255);not null" json:"reason"`
	OldMicros  int64  `gorm:"not null" json:"oldMicros"`
	NewMicros  int64  `gorm:"not null" json:"newMicros"`
	// Absolute recognition discount against this source, applied after refunds.
	DiscountMicros int64     `gorm:"not null" json:"discountMicros"`
	RefundIDs      string    `gorm:"type:text" json:"refundIds"`
	CreatedAt      time.Time `json:"createdAt"`
}

// EffectiveRebuildAudit records explicit operator-approved batches. It never
// materializes a synthetic payment, refund, bill, or opening balance.
type EffectiveRebuildAudit struct {
	ID         int64     `gorm:"primaryKey" json:"id"`
	OperatorID int32     `gorm:"not null" json:"operatorId"`
	AfterID    int32     `gorm:"not null" json:"afterId"`
	NextID     int32     `gorm:"not null" json:"nextId"`
	Examined   int       `gorm:"not null" json:"examined"`
	Changed    int       `gorm:"not null" json:"changed"`
	Skipped    int       `gorm:"not null" json:"skipped"`
	Digest     string    `gorm:"type:char(64);not null" json:"digest"`
	Reason     string    `gorm:"type:varchar(255);not null" json:"reason"`
	CreatedAt  time.Time `json:"createdAt"`
}
