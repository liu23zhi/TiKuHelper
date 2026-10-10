package entity

import "time"

const TableNameAIModelArchitecture = "ai_model_architectures"

// AIModelArchitecture 现在只描述「模型绑定」信息（名称、绑定的上游模型名、启停与排序）。
// 历史版本中的 input_token_price / output_token_price / billing_multiplier 列已废弃：
// 模型计费统一由 ai_model_prices（模型价格表）决定。
type AIModelArchitecture struct {
	ID        int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string    `gorm:"column:name;type:varchar(128);not null" json:"name"`
	ModelName string    `gorm:"column:model_name;type:varchar(128);index" json:"modelName"`
	Enabled   bool      `gorm:"column:enabled;not null" json:"enabled"`
	Sort      int       `gorm:"column:sort;not null;default:0" json:"sort"`
	Remark    string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIModelArchitecture) TableName() string {
	return TableNameAIModelArchitecture
}
