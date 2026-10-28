package entity

import "time"

const TableNameAIModelPrice = "ai_model_prices"

// AIModelPrice 是 AI 计费使用的模型价格表。
//
// 四个单价单位统一为「人民币（元）/ 百万 token」，不再涉及美元与汇率换算：
// 上游按美元报价时，请先自行折算成人民币后填入。
//
// 计费口径：
//
//	未命中缓存的输入 × InputPrice
//	+ 命中缓存的输入 × CachedPrice
//	+ 缓存写入 token × CacheWritePrice
//	+ 输出 × OutputPrice
//
// 四项合计之后再乘以 Multiplier（倍率，默认 1），
// 这样临时加价/打折只需调整倍率，不必改动单价本身。
type AIModelPrice struct {
	ID                  int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelName           string    `gorm:"column:model_name;type:varchar(128);not null;uniqueIndex" json:"modelName"`
	DisplayName         string    `gorm:"column:display_name;type:varchar(128)" json:"displayName"`
	ContextWindowTokens int64     `gorm:"column:context_window_tokens;not null;default:0" json:"contextWindowTokens"`
	InputPrice          float64   `gorm:"column:input_price;type:decimal(18,8);not null;default:0" json:"inputPrice"`
	CachedPrice         float64   `gorm:"column:cached_price;type:decimal(18,8);not null;default:0" json:"cachedPrice"`
	CacheWritePrice     float64   `gorm:"column:cache_write_price;type:decimal(18,8);not null;default:0" json:"cacheWritePrice"`
	OutputPrice         float64   `gorm:"column:output_price;type:decimal(18,8);not null;default:0" json:"outputPrice"`
	Multiplier          float64   `gorm:"column:multiplier;type:decimal(18,6);not null;default:1" json:"multiplier"`
	Status              string    `gorm:"column:status;type:varchar(32);not null;default:enabled" json:"status"`
	Sort                int       `gorm:"column:sort;not null;default:0" json:"sort"`
	Remark              string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	IsSeed              bool      `gorm:"column:is_seed;not null;default:false" json:"isSeed"`
	IsUserModified      bool      `gorm:"column:is_user_modified;not null;default:false" json:"isUserModified"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt           time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIModelPrice) TableName() string {
	return TableNameAIModelPrice
}
