package entity

import "time"

const TableNameAISearchConfig = "ai_search_configs"

type AISearchConfig struct {
	ID               int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider         string    `gorm:"column:provider;type:varchar(64);uniqueIndex;not null" json:"provider"`
	Enabled          bool      `gorm:"column:enabled;not null;default:false" json:"enabled"`
	BaseURL          string    `gorm:"column:base_url;type:varchar(255)" json:"baseUrl"`
	APIKey           string    `gorm:"column:api_key;type:text" json:"apiKey"`
	Model            string    `gorm:"column:model;type:varchar(128)" json:"model"`
	ManualModels     string    `gorm:"column:manual_models;type:text" json:"manualModels"`
	Prompt           string    `gorm:"column:prompt;type:text" json:"prompt"`
	Remark           string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	Temperature      float64   `gorm:"column:temperature;type:double" json:"temperature"`
	TopP             float64   `gorm:"column:top_p;type:double" json:"topP"`
	InputTokenPrice  float64   `gorm:"column:input_token_price;type:decimal(18,8);not null;default:0" json:"inputTokenPrice"`
	OutputTokenPrice float64   `gorm:"column:output_token_price;type:decimal(18,8);not null;default:0" json:"outputTokenPrice"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (*AISearchConfig) TableName() string {
	return TableNameAISearchConfig
}
