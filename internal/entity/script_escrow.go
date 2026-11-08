package entity

import "time"

// ScriptEscrow is the remaining, spendable reservation for the new paid
// script mode. Legacy UserScript.ReservedFee is deliberately not migrated
// into it: that field represents a different, fully refundable reservation.
type ScriptEscrow struct {
	ScriptID        int32     `gorm:"column:script_id;primaryKey;autoIncrement:false" json:"scriptId"`
	UserID          int32     `gorm:"column:user_id;not null;index" json:"userId"`
	AvailableMicros int64     `gorm:"column:available_micros;not null" json:"availableMicros"`
	Revision        int64     `gorm:"column:revision;not null" json:"-"`
	CreatedAt       time.Time `json:"-"`
	UpdatedAt       time.Time `json:"-"`
}

func (*ScriptEscrow) TableName() string { return "script_escrows" }

// ScriptEscrowEntry is an immutable transfer/settlement receipt. Amounts are
// integer millionths of a yuan; an operation key is stored only as a digest.
type ScriptEscrowEntry struct {
	ID                 int64  `gorm:"primaryKey;autoIncrement"`
	ScriptID           int32  `gorm:"column:script_id;not null;uniqueIndex:idx_script_escrow_operation,priority:1"`
	UserID             int32  `gorm:"column:user_id;not null;index"`
	OperationHash      string `gorm:"column:operation_hash;type:char(64);not null;uniqueIndex:idx_script_escrow_operation,priority:2"`
	Kind               string `gorm:"column:kind;type:varchar(16);not null"`
	AmountMicros       int64  `gorm:"column:amount_micros;not null"`
	EscrowBeforeMicros int64  `gorm:"column:escrow_before_micros;not null"`
	EscrowAfterMicros  int64  `gorm:"column:escrow_after_micros;not null"`
	WalletBeforeMicros int64  `gorm:"column:wallet_before_micros;not null"`
	WalletAfterMicros  int64  `gorm:"column:wallet_after_micros;not null"`
	CreatedAt          time.Time
}

func (*ScriptEscrowEntry) TableName() string { return "script_escrow_entries" }
