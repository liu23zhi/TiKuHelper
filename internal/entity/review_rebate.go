package entity

import "time"

const (
	TableNameReviewRebateProgram = "review_rebate_programs"
	TableNameReviewRebateRule    = "review_rebate_rules"
	TableNameReviewRebateClaim   = "review_rebate_claims"
	TableNameReviewRebateAward   = "review_rebate_awards"

	// 提交单状态。语义见 task_doc/16-评价返现与邀请返现-设计定稿.md §1.6。
	//
	// 关键区分：**没有任何状态代表「已发放」之外的可消费余额变化** ——
	// 只有 approved 会碰钱包，其余状态都只影响这一行的展示与后续可否复核。
	ReviewRebateStatusSubmitted      = "submitted"       // 已收截图，等待抓取/打分
	ReviewRebateStatusScraping       = "scraping"        // 正在抓取公开评价（为将来的异步链路预留）
	ReviewRebateStatusScored         = "scored"          // Agent 已打分且置信度达标，等待人工复核
	ReviewRebateStatusPendingReview  = "pending_review"  // 置信度低于门槛，强制人工判断
	ReviewRebateStatusApproved       = "approved"        // 管理员批准，已发放
	ReviewRebateStatusRejected       = "rejected"        // 管理员拒绝，不发放
	ReviewRebateStatusDuplicate      = "duplicate"       // 同一账号同一评价重复提交（不落新行，见服务层注释）
	ReviewRebateStatusLimitReached   = "limit_reached"   // 次数或金额上限（不落新行，见服务层注释）
	ReviewRebateStatusBelowThreshold = "below_threshold" // 规则表未命中，金额为 0

	// ReviewRebateGiftSource 是赠金流水里的来源标记。
	//
	// 它与邀请返佣、注册赠送共用一个 gift_balance 桶，靠这个字段分来源。
	ReviewRebateGiftSource = "review_rebate"
)

// ReviewRebateProgram 是一个评价返现活动的配置。
//
// 换成新版活动时**不要删旧行**：claims/awards 通过 program_id 引用它，
// 删掉会让历史提交失去配置上下文（换版不重置已领次数的前提就是活动行稳定）。
type ReviewRebateProgram struct {
	ID   int32  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name string `gorm:"column:name;type:varchar(128);not null;default:''" json:"name"`
	// StoreURL 是商店条目地址，展示给用户「去哪儿评价」。
	StoreURL string `gorm:"column:store_url;type:varchar(512);not null;default:''" json:"storeUrl"`
	// ExtensionID 限定抓取范围：只读这个扩展条目的公开评价。
	ExtensionID string `gorm:"column:extension_id;type:varchar(128);not null;default:''" json:"extensionId"`
	// Enabled 是活动开关。与系统设置里的总开关是**与**关系：
	// 总开关关闭时任何活动都不生效（入口直接拒绝）。
	Enabled bool `gorm:"column:enabled;not null;default:false" json:"enabled"`
	// MaxClaimsPerUser 是每账号累计提交次数上限（默认 3）。
	MaxClaimsPerUser int `gorm:"column:max_claims_per_user;type:int(11);not null;default:3" json:"maxClaimsPerUser"`
	// MaxAmountPerUser 是每账号累计发放金额上限（默认 5.04，与 04 稿 10.4.2 的 1.68×3 对齐）。
	MaxAmountPerUser float64 `gorm:"column:max_amount_per_user;type:decimal(18,6);not null;default:5.04" json:"maxAmountPerUser"`
	// MinAgentConfidence 低于此值时强制人工复核（默认 0.6）。
	MinAgentConfidence float64 `gorm:"column:min_agent_confidence;type:decimal(9,6);not null;default:0.6" json:"minAgentConfidence"`
	// TutorialVersionID 关联图文教程版本（0 = 未关联）。
	TutorialVersionID int32 `gorm:"column:tutorial_version_id;type:int(11);not null;default:0" json:"tutorialVersionId"`
	// PolicyVersion 是规则口径版本快照，写进发放流水，改版不重算已发放。
	PolicyVersion string    `gorm:"column:policy_version;type:varchar(32);not null;default:''" json:"policyVersion"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*ReviewRebateProgram) TableName() string { return TableNameReviewRebateProgram }

// ReviewRebateRule 是规则表的一行：星数区间 × 内容分区间 → 金额。
//
// 区间**允许重叠**，靠 Priority 决断：优先级高的先命中（降序取第一条）。
// 这就是「1星+1分 → 0.001，5星+8~10分 → 0.8，5星+10分 → 1.0」能同时存在的原因。
type ReviewRebateRule struct {
	ID        int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	ProgramID int32 `gorm:"column:program_id;type:int(11);not null;index" json:"programId"`
	// MinStars/MaxStars 是星数闭区间，取值 1–5。
	MinStars int `gorm:"column:min_stars;type:int(11);not null;default:1" json:"minStars"`
	MaxStars int `gorm:"column:max_stars;type:int(11);not null;default:5" json:"maxStars"`
	// MinContentScore/MaxContentScore 是内容分闭区间，取值 1–10。
	MinContentScore int `gorm:"column:min_content_score;type:int(11);not null;default:1" json:"minContentScore"`
	MaxContentScore int `gorm:"column:max_content_score;type:int(11);not null;default:10" json:"maxContentScore"`
	// Amount 是命中时建议发放的金额（6 位小数）。0 表示「命中但不发钱」。
	Amount float64 `gorm:"column:amount;type:decimal(18,6);not null;default:0" json:"amount"`
	// Priority 是命中优先级：数值越大越先生效。
	Priority int `gorm:"column:priority;type:int(11);not null;default:0" json:"priority"`
	// Enabled 默认 **false**（fail-closed）：新建规则必须显式启用才参与匹配。
	//
	// 这里不能写 default:true —— GORM 对带 default 标签的列会**忽略零值**，
	// 于是「显式创建一条 Enabled=false 的规则」会被静默写入 true，
	// 管理页上看到「已停用」但实际仍在发钱。默认 false 才是安全方向：
	// 漏写 enabled 只会少发，不会多发。admin 接口在缺省时显式给 true。
	Enabled   bool      `gorm:"column:enabled;not null;default:false" json:"enabled"`
	Remark    string    `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*ReviewRebateRule) TableName() string { return TableNameReviewRebateRule }

// ReviewRebateClaim 是一次用户提交（一张截图对应一条）。
//
// 关于去重：唯一索引 (program_id, user_id, canonical_claim_id) 是账务层去重，
//
//	改文字、重传图都不算新申请。duplicate / limit_reached 两种状态**不落新行**
//	（唯一索引不允许前者，后者是不想让表被重复尝试灌满），服务层用哨兵错误返回，
//	详见 review_rebate.go 里 CreateReviewRebateClaimWithDB 的注释。
//
// 关于归属：本表**不**断言「这条评价就是这个人写的」。Agent 输出只表示
// 「公开评价里存在一条与截图高度一致的记录」的置信度，低置信度强制人工复核。
type ReviewRebateClaim struct {
	ID        int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	ProgramID int32 `gorm:"column:program_id;type:int(11);not null;index;uniqueIndex:uk_review_rebate_claim_canonical,priority:1" json:"programId"`
	UserID    int32 `gorm:"column:user_id;type:int(11);not null;index;uniqueIndex:uk_review_rebate_claim_canonical,priority:2" json:"userId"`
	// ScreenshotPath 是相对 data/review-rebate 的私有路径，**不是公开 URL**。
	ScreenshotPath string `gorm:"column:screenshot_path;type:varchar(255);not null;default:''" json:"-"`
	// ScreenshotSHA256 是截图内容摘要。只作线索，**不能当唯一键**：
	// 重新编码会改摘要，用它判重会误伤（04 稿 10.4.4）。
	ScreenshotSHA256 string `gorm:"column:screenshot_sha256;type:varchar(64);not null;default:'';index" json:"-"`
	// ClaimedStars 是用户自填星数（0 = 未填），仅供参考。
	ClaimedStars int `gorm:"column:claimed_stars;type:int(11);not null;default:0" json:"claimedStars"`
	// ClaimedReviewURL 是用户可选填的评价链接。
	ClaimedReviewURL string `gorm:"column:claimed_review_url;type:varchar(512);not null;default:''" json:"claimedReviewUrl"`
	// CanonicalClaimID 是规范化摘要，用于同账号去重（唯一索引的一部分）。
	CanonicalClaimID string `gorm:"column:canonical_claim_id;type:varchar(64);not null;default:'';uniqueIndex:uk_review_rebate_claim_canonical,priority:3" json:"-"`
	Status           string `gorm:"column:status;type:varchar(24);not null;default:'submitted';index" json:"status"`
	// 以下是 Agent 输出快照（只作建议，写入时不发放任何金额）。
	AgentStars        int     `gorm:"column:agent_stars;type:int(11);not null;default:0" json:"agentStars"`
	AgentContentScore int     `gorm:"column:agent_content_score;type:int(11);not null;default:0" json:"agentContentScore"`
	AgentConfidence   float64 `gorm:"column:agent_confidence;type:decimal(9,6);not null;default:0" json:"agentConfidence"`
	// AgentRationale 存模型给出的结构化理由（JSON 或自然语言），仅管理员可见。
	AgentRationale string `gorm:"column:agent_rationale;type:text" json:"-"`
	// AgentScorer 标记这条分数是谁给的：agent=真实模型，stub=未配置模型时的占位实现，
	// 空字符串=历史行或注入式打分器（无法归因）。
	//
	// 这一列存在的唯一目的：管理端必须能区分「模型看过截图」与「占位实现按哈希派生」。
	// 分不出来的后果是运营把哈希算出来的 5 星当成核验结论，直接点批准。
	AgentScorer string `gorm:"column:agent_scorer;type:varchar(16);not null;default:''" json:"agentScorer"`
	// MatchedRuleID 是打分时命中的规则快照；规则被删后这里会指向不存在的行，
	// 金额已经写进 AwardAmount，不影响对账。
	MatchedRuleID int32 `gorm:"column:matched_rule_id;type:int(11);not null;default:0" json:"matchedRuleId"`
	// AwardAmount 是**候选**金额（命中规则的金额），批准时才真正入账。
	AwardAmount float64 `gorm:"column:award_amount;type:decimal(18,6);not null;default:0" json:"awardAmount"`
	// ReviewNote 是管理员理由（拒绝时必填项由运营约定，不做代码强制）。
	ReviewNote       string     `gorm:"column:review_note;type:varchar(255);not null;default:''" json:"reviewNote"`
	ReviewedByUserID int32      `gorm:"column:reviewed_by_user_id;type:int(11);not null;default:0" json:"reviewedByUserId"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime;index" json:"createdAt"`
	ReviewedAt       *time.Time `gorm:"column:reviewed_at" json:"reviewedAt,omitempty"`
}

func (*ReviewRebateClaim) TableName() string { return TableNameReviewRebateClaim }

// ReviewRebateAward 是发放流水（一笔批准一条）。
//
// ClaimID 唯一是**账务去重的最后一道闸**：重复批准、并发批准、回调重放
// 都只可能留下一条 award，钱包不会二次累加。
type ReviewRebateAward struct {
	ID      int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	ClaimID int32 `gorm:"column:claim_id;type:int(11);not null;uniqueIndex" json:"claimId"`
	UserID  int32 `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	// Amount 是实际入账金额（6 位小数），等于批准时的 claim.award_amount。
	Amount float64 `gorm:"column:amount;type:decimal(18,6);not null;default:0" json:"amount"`
	// GiftSource 固定 "review_rebate"，与其它赠金来源区分。
	GiftSource string `gorm:"column:gift_source;type:varchar(32);not null;default:''" json:"giftSource"`
	// PolicyVersion 是发放时的规则口径快照。
	PolicyVersion string    `gorm:"column:policy_version;type:varchar(32);not null;default:''" json:"policyVersion"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ReviewRebateAward) TableName() string { return TableNameReviewRebateAward }
