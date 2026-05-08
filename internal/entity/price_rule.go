package entity

import "time"

const TableNamePriceRule = "price_rules"

type PriceRule struct {
	ID               int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Type             string    `gorm:"column:type;type:varchar(32);not null;index" json:"type"`
	Provider         string    `gorm:"column:provider;type:varchar(64)" json:"provider"`
	ModelName        string    `gorm:"column:model_name;type:varchar(128)" json:"modelName"`
	Unit             string    `gorm:"column:unit;type:varchar(32);not null" json:"unit"`
	InputTokenPrice  float64   `gorm:"column:input_token_price;type:decimal(18,8)" json:"inputTokenPrice"`
	OutputTokenPrice float64   `gorm:"column:output_token_price;type:decimal(18,8)" json:"outputTokenPrice"`
	FlatPrice        float64   `gorm:"column:flat_price;type:decimal(18,8)" json:"flatPrice"`
	Status           string    `gorm:"column:status;type:varchar(32);not null;default:enabled" json:"status"`
	Remark           string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*PriceRule) TableName() string {
	return TableNamePriceRule
}
