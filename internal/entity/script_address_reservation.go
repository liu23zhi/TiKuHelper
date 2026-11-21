package entity

// ScriptAddressReservation keeps addresses assigned even after authorization
// expiry, replacement, or script deletion. Its key is shared by both launchers.
type ScriptAddressReservation struct {
	AddressKey string `gorm:"column:address_key;type:char(64);primaryKey"`
	ScriptID   int32  `gorm:"column:script_id;not null"`
	Kind       string `gorm:"column:kind;type:varchar(5);not null"`
}

func (ScriptAddressReservation) TableName() string { return "script_address_reservations" }
