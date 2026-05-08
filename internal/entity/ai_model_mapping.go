package entity

import "time"

const TableNameAIModelMapping = "ai_model_mappings"

type AIModelMapping struct {
	ID                       int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelName                string    `gorm:"column:model_name;type:varchar(128);uniqueIndex;not null" json:"modelName"`
	ArchitectureID           int32     `gorm:"column:architecture_id;not null;index" json:"architectureId"`
	InputTokenPriceOverride  *float64  `gorm:"column:input_token_price_override;type:decimal(18,8)" json:"inputTokenPriceOverride"`
	OutputTokenPriceOverride *float64  `gorm:"column:output_token_price_override;type:decimal(18,8)" json:"outputTokenPriceOverride"`
	Enabled                  bool      `gorm:"column:enabled;not null" json:"enabled"`
	Remark                   string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt                time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt                time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIModelMapping) TableName() string {
	return TableNameAIModelMapping
}
