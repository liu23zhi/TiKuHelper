package entity

import "time"

const TableNameAIModelPrice = "ai_model_prices"

// AIModelPrice 是 AI 计费使用的模型价格表。
// 三个单价单位统一为「人民币（元）/ 百万 token」，不再涉及美元与汇率换算：
// 上游按美元报价时，请先自行折算成人民币后填入。
type AIModelPrice struct {
	ID             int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelName      string    `gorm:"column:model_name;type:varchar(128);not null;uniqueIndex" json:"modelName"`
	DisplayName    string    `gorm:"column:display_name;type:varchar(128)" json:"displayName"`
	InputPrice     float64   `gorm:"column:input_price;type:decimal(18,8);not null;default:0" json:"inputPrice"`
	CachedPrice    float64   `gorm:"column:cached_price;type:decimal(18,8);not null;default:0" json:"cachedPrice"`
	OutputPrice    float64   `gorm:"column:output_price;type:decimal(18,8);not null;default:0" json:"outputPrice"`
	Status         string    `gorm:"column:status;type:varchar(32);not null;default:enabled" json:"status"`
	Sort           int       `gorm:"column:sort;not null;default:0" json:"sort"`
	Remark         string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	IsSeed         bool      `gorm:"column:is_seed;not null;default:false" json:"isSeed"`
	IsUserModified bool      `gorm:"column:is_user_modified;not null;default:false" json:"isUserModified"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIModelPrice) TableName() string {
	return TableNameAIModelPrice
}
