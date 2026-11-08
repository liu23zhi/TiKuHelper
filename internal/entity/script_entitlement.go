package entity

import "time"

const TableNameScriptActivationEntitlement = "script_activation_entitlements"

// ScriptActivationEntitlement is an account-bound, one-time script grant.
// A wallet purchase never writes the plaintext code, its signing key or a
// runtime bearer credential. A NULL ScriptID means the grant has not yet
// been spent on a script; the two activation fields are set atomically only
// after a successful session insert.
type ScriptActivationEntitlement struct {
	ID                 int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID             int32      `gorm:"column:user_id;not null;uniqueIndex:idx_script_activation_purchase,priority:1" json:"userId"`
	PurchaseKeyHash    string     `gorm:"column:purchase_key_hash;type:char(64);not null;uniqueIndex:idx_script_activation_purchase,priority:2" json:"-"`
	PurchaseNonce      string     `gorm:"column:purchase_nonce;type:varchar(64);not null" json:"-"`
	CodeHash           string     `gorm:"column:code_hash;type:char(64);not null;uniqueIndex" json:"-"`
	PurchasePrice      float64    `gorm:"column:purchase_price;type:decimal(18,6);not null" json:"purchasePrice"`
	RedeemedAt         *time.Time `gorm:"column:redeemed_at" json:"redeemedAt,omitempty"`
	ScriptID           *int32     `gorm:"column:script_id;uniqueIndex" json:"scriptId,omitempty"`
	ScriptBoundAt      *time.Time `gorm:"column:script_bound_at" json:"scriptBoundAt,omitempty"`
	ActivationTicketID *int64     `gorm:"column:activation_ticket_id;uniqueIndex" json:"-"`
	RunSessionID       *int64     `gorm:"column:run_session_id;uniqueIndex" json:"-"`
	RunConsumedAt      *time.Time `gorm:"column:run_consumed_at" json:"runConsumedAt,omitempty"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptActivationEntitlement) TableName() string { return TableNameScriptActivationEntitlement }
