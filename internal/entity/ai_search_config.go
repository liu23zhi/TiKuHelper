package entity

import "time"

const TableNameAISearchConfig = "ai_search_configs"

// AISearchConfig 保存默认 AI 搜索账号配置。
//
// input_token_price / output_token_price / exchange_rate /
// use_global_billing_multiplier / global_billing_multiplier 为历史倍率计费字段，
// 已不再参与计费，保留列仅为兼容旧库与历史账单；模型单价统一由
// ai_model_prices（模型价格表）决定。
type AISearchConfig struct {
	ID                         int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider                   string    `gorm:"column:provider;type:varchar(64);uniqueIndex;not null" json:"provider"`
	Enabled                    bool      `gorm:"column:enabled;not null;default:false" json:"enabled"`
	BaseURL                    string    `gorm:"column:base_url;type:varchar(255)" json:"baseUrl"`
	APIKey                     string    `gorm:"column:api_key;type:text" json:"apiKey"`
	Model                      string    `gorm:"column:model;type:varchar(128)" json:"model"`
	BackupModel                string    `gorm:"column:backup_model;type:varchar(128)" json:"backupModel"`
	ManualModels               string    `gorm:"column:manual_models;type:text" json:"manualModels"`
	Prompt                     string    `gorm:"column:prompt;type:text" json:"prompt"`
	Remark                     string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	Temperature                float64   `gorm:"column:temperature;type:double" json:"temperature"`
	TopP                       float64   `gorm:"column:top_p;type:double" json:"topP"`
	InputTokenPrice            float64   `gorm:"column:input_token_price;type:decimal(18,8);not null;default:0" json:"-"`
	OutputTokenPrice           float64   `gorm:"column:output_token_price;type:decimal(18,8);not null;default:0" json:"-"`
	ExchangeRate               float64   `gorm:"column:exchange_rate;type:decimal(18,6);not null;default:1" json:"-"`
	UseGlobalBillingMultiplier bool      `gorm:"column:use_global_billing_multiplier;not null;default:true" json:"-"`
	GlobalBillingMultiplier    float64   `gorm:"column:global_billing_multiplier;type:decimal(18,4);not null;default:1" json:"-"`
	CreatedAt                  time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt                  time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (*AISearchConfig) TableName() string {
	return TableNameAISearchConfig
}
