package entity

import "time"

const TableNameAIModelArchitecture = "ai_model_architectures"

type AIModelArchitecture struct {
	ID               int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name             string    `gorm:"column:name;type:varchar(128);not null" json:"name"`
	ModelName        string    `gorm:"column:model_name;type:varchar(128);index" json:"modelName"`
	InputTokenPrice  float64   `gorm:"column:input_token_price;type:decimal(18,8);not null;default:0" json:"inputTokenPrice"`
	OutputTokenPrice float64   `gorm:"column:output_token_price;type:decimal(18,8);not null;default:0" json:"outputTokenPrice"`
	Enabled          bool      `gorm:"column:enabled;not null" json:"enabled"`
	Sort             int       `gorm:"column:sort;not null;default:0" json:"sort"`
	Remark           string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIModelArchitecture) TableName() string {
	return TableNameAIModelArchitecture
}
