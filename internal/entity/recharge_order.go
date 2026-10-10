package entity

import "time"

const TableNameRechargeOrder = "recharge_orders"

type RechargeOrder struct {
	ID                        int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                    int32      `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	OrderNo                   string     `gorm:"column:order_no;type:varchar(64);uniqueIndex;not null" json:"orderNo"`
	PaymentProvider           string     `gorm:"column:payment_provider;type:varchar(64);not null" json:"paymentProvider"`
	PaymentMethodNameSnapshot string     `gorm:"column:payment_method_name_snapshot;type:varchar(128);not null;default:''" json:"paymentMethodNameSnapshot"`
	Amount                    float64    `gorm:"column:amount;type:decimal(18,6);not null" json:"amount"`
	Status                    string     `gorm:"column:status;type:varchar(32);not null;default:pending" json:"status"`
	ThirdPartyOrderNo         string     `gorm:"column:third_party_order_no;type:varchar(128)" json:"thirdPartyOrderNo"`
	PaidAt                    *time.Time `gorm:"column:paid_at" json:"paidAt"`
	CallbackPayload           string     `gorm:"column:callback_payload;type:longtext" json:"callbackPayload"`
	CreatedAt                 time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt                 time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*RechargeOrder) TableName() string {
	return TableNameRechargeOrder
}
