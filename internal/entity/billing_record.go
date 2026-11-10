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
	// GiftCharged / PrincipalCharged 记录这一笔消费**分别从哪个桶扣了多少**。
	//
	// 双桶模型（43-*.md 规则 2）：消费先扣 gift_balance，扣光再扣 balance。
	// 这两列是「已消耗本金」的唯一可靠来源 —— 退款可退额 = 充值实付本金 −
	// 已消耗本金（赠金消耗不影响可退额），没有它们就只能靠猜。
	//
	// 历史行（加列之前落账的）两列都是 0，读侧必须按「未知」处理而不是「没扣本金」：
	// 旧模型下 balance 是总可用，无法还原分桶，任何推断都会写错钱包口径。
	GiftCharged      float64 `gorm:"column:gift_charged;type:decimal(18,8);not null;default:0" json:"giftCharged"`
	PrincipalCharged float64 `gorm:"column:principal_charged;type:decimal(18,8);not null;default:0" json:"principalCharged"`
	// The default keeps historical records visible without a data backfill.
	HiddenFromStatement bool   `gorm:"column:hidden_from_statement;not null;default:false" json:"-"`
	OperatorID          int32  `gorm:"column:operator_id;type:int(11);not null;default:0" json:"operatorId"`
	OperatorUsername    string `gorm:"column:operator_username;type:varchar(255)" json:"operatorUsername"`
	AdjustmentMode      string `gorm:"column:adjustment_mode;type:varchar(16)" json:"adjustmentMode"`
	RequestID           string `gorm:"column:request_id;type:varchar(64);index" json:"requestId"`
	// SearchMode 记录这笔消费对应的搜索模式：deep=深度思考，normal=普通搜索。
	// 只有搜题类账单会写；充值/赠送以及加列之前的历史行为空（界面降级为「-」）。
	SearchMode string `gorm:"column:search_mode;type:varchar(16)" json:"searchMode"`
	// ClientIP 是产生这笔账单的请求来源 IP，供账单明细展示归属地。
	//
	// 新请求在落账时一并写入；启动迁移会对日志中能精确匹配 requestId 的历史行尽力回填。
	ClientIP       string    `gorm:"column:client_ip;type:varchar(64)" json:"clientIp"`
	RelatedOrderID int32     `gorm:"column:related_order_id;type:int(11);index" json:"relatedOrderId"`
	Remark         string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*BillingRecord) TableName() string {
	return TableNameBillingRecord
}
