package entity

import "time"

const TableNameBillingRecord = "billing_records"

type BillingRecord struct {
	ID                 int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID             int32     `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	AccessTokenID      int32     `gorm:"column:access_token_id;type:int(11);index" json:"accessTokenId"`
	Type               string    `gorm:"column:type;type:varchar(32);not null;index" json:"type"`
	UsedDefaultAccount bool      `gorm:"column:used_default_account;not null;default:false" json:"usedDefaultAccount"`
	Provider           string    `gorm:"column:provider;type:varchar(64)" json:"provider"`
	ModelName          string    `gorm:"column:model_name;type:varchar(128)" json:"modelName"`
	InputTokens        int64     `gorm:"column:input_tokens;type:bigint;not null;default:0" json:"inputTokens"`
	OutputTokens       int64     `gorm:"column:output_tokens;type:bigint;not null;default:0" json:"outputTokens"`
	TotalTokens        int64     `gorm:"column:total_tokens;type:bigint;not null;default:0" json:"totalTokens"`
	UnitPriceSnapshot  string    `gorm:"column:unit_price_snapshot;type:text" json:"unitPriceSnapshot"`
	Amount             float64   `gorm:"column:amount;type:decimal(18,8);not null;default:0" json:"amount"`
	BalanceBefore      float64   `gorm:"column:balance_before;type:decimal(18,2);not null;default:0" json:"balanceBefore"`
	BalanceAfter       float64   `gorm:"column:balance_after;type:decimal(18,2);not null;default:0" json:"balanceAfter"`
	RequestID          string    `gorm:"column:request_id;type:varchar(64);index" json:"requestId"`
	RelatedOrderID     int32     `gorm:"column:related_order_id;type:int(11);index" json:"relatedOrderId"`
	Remark             string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt          time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*BillingRecord) TableName() string {
	return TableNameBillingRecord
}
