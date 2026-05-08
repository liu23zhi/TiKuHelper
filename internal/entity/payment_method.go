package entity

import "time"

const TableNamePaymentMethod = "payment_methods"

type PaymentMethod struct {
	ID        int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string    `gorm:"column:name;type:varchar(128);not null" json:"name"`
	IconSVG   string    `gorm:"column:icon_svg;type:longtext" json:"iconSvg"`
	EPayType  string    `gorm:"column:epay_type;type:varchar(64);not null;index" json:"epayType"`
	Enabled   bool      `gorm:"column:enabled;not null;index" json:"enabled"`
	Sort      int       `gorm:"column:sort;not null;default:0;index" json:"sort"`
	Remark    string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*PaymentMethod) TableName() string {
	return TableNamePaymentMethod
}
