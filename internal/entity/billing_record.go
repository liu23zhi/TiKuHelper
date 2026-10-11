package entity

import "time"

const TableNameBillingRecord = "billing_records"

type BillingRecord struct {
	ID                 int32  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID             int32  `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	AccessTokenID      int32  `gorm:"column:access_token_id;type:int(11);index" json:"accessTokenId"`
	Type               string `gorm:"column:type;type:varchar(32);not null;index" json:"type"`
	UsedDefaultAccount bool   `gorm:"column:used_default_account;not null;default:false" json:"usedDefaultAccount"`
	Provider           string `gorm:"column:provider;type:varchar(64)" json:"provider"`
	ModelName          string `gorm:"column:model_name;type:varchar(128)" json:"modelName"`
	InputTokens        int64  `gorm:"column:input_tokens;type:bigint;not null;default:0" json:"inputTokens"`
	CachedInputTokens  int64  `gorm:"column:cached_input_tokens;type:bigint;not null;default:0" json:"cachedInputTokens"`
	OutputTokens       int64  `gorm:"column:output_tokens;type:bigint;not null;default:0" json:"outputTokens"`
	TotalTokens        int64  `gorm:"column:total_tokens;type:bigint;not null;default:0" json:"totalTokens"`
	UnitPriceSnapshot  string `gorm:"column:unit_price_snapshot;type:text" json:"unitPriceSnapshot"`
	// QuestionText 是题目原文的冗余副本，专供账单列表按题目搜索。
	// 历史行由启动时的一次性回溯填充（见 registry.ensureBillingQuestionTextSchema）。
	QuestionText  string  `gorm:"column:question_text;type:varchar(512);index" json:"questionText"`
	Amount        float64 `gorm:"column:amount;type:decimal(18,8);not null;default:0" json:"amount"`
	BalanceBefore float64 `gorm:"column:balance_before;type:decimal(18,6);not null;default:0" json:"balanceBefore"`
	BalanceAfter  float64 `gorm:"column:balance_after;type:decimal(18,6);not null;default:0" json:"balanceAfter"`
	RequestID     string  `gorm:"column:request_id;type:varchar(64);index" json:"requestId"`
	// ClientIP 是产生这笔账单的请求来源 IP，供账单明细展示归属地。
	//
	// logs 表没有 request_id，无法事后关联，因此必须在落账时一并写入；
	// 历史行无法回溯（见 registry.ensureBillingClientIPSchema）。
	ClientIP       string    `gorm:"column:client_ip;type:varchar(64)" json:"clientIp"`
	RelatedOrderID int32     `gorm:"column:related_order_id;type:int(11);index" json:"relatedOrderId"`
	Remark         string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*BillingRecord) TableName() string {
	return TableNameBillingRecord
}
