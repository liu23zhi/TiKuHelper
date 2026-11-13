package entity

import "time"

const (
	// TableNameRiskRule 是风控规则表（96-风控管理仅告警与用户画像-设计.md / 121 §4.7）。
	TableNameRiskRule = "risk_rules"
	// TableNameRiskWarning 是风控告警记录表（96 §2.3 / 121 §4.6）。
	//
	// 表名用 risk_warnings 而不是 118 号里建议的 risk_alert_*：
	// 119-风控系统溯源报告.md §4.2 明确「命名以 96 号为准」。
	TableNameRiskWarning = "risk_warnings"
)

// 严重级别（96 §2.1 的级别列：high / medium / low）。
const (
	RiskSeverityHigh   = "high"
	RiskSeverityMedium = "medium"
	RiskSeverityLow    = "low"
)

// 三条欠款规则的 rule_id（118-决策记录第十二轮-欠款与风控告警.md §2.4）。
//
// 命名刻意与 96 §2.2 的配置键风格对齐（`risk_rule_<rule_id>_threshold`），
// 因此这里用短名而不是 96 §2.1 里 `multi_account_ip` 那种完整描述名 ——
// 阈值键越长越容易写错，而键名是运营要手工改的东西。
const (
	RiskRuleIDDebtUser   = "debt_user"
	RiskRuleIDDebtBatch  = "debt_batch"
	RiskRuleIDDebtSystem = "debt_system"
)

// RiskRuleDefault 是一条规则的**代码内默认值**。
//
// 放 entity 而不是 registry/service，是因为两处都要用它：
//   - registry 用它写种子（risk_rules 的初始行）；
//   - service 用它兜底（规则行被人工删/改空时仍然能判定，不能静默不告警）。
//
// 若放在 service，registry 引用 service 会形成 import 环。
type RiskRuleDefault struct {
	RuleID   string
	Name     string
	Severity string
	// Threshold 是阈值默认值（96 §2.2：默认值写在代码里，不在 DB 里）。
	Threshold float64
	// ThrottleSeconds 是节流语义（见 RiskRule.ThrottleSeconds 的注释）。
	ThrottleSeconds int
	Description     string
}

// DefaultRiskDebtRules 是三条欠款规则的默认值（118 §2.4 / §2.5）。
//
// 阈值一律 100 / 1000 / 5000，且都**可配置**（`risk_rule_<rule_id>_threshold`）。
//
// ⚠️ 三条规则默认 Enabled=true，这与 96 §7「风控规则默认关闭」不一致，是有意的：
// 96 §7 说的是 R1–R8 那批**行为**规则（撞库、刷单等，误报会打扰运营）；
// 而欠款告警是「欠款不设上限」的唯一安全阀（118 §2.9），默认关掉等于没有安全阀。
// 每条规则仍可在 risk_rules 行里单独停用（Enabled 列），也可以改阈值。
var DefaultRiskDebtRules = []RiskRuleDefault{
	{
		RuleID:   RiskRuleIDDebtUser,
		Name:     "单用户欠款超阈值",
		Severity: RiskSeverityMedium,
		// 118 §2.4 触发 1：单用户 |balance| >= 100 元，同一用户 24h 只告警一次。
		Threshold:       100,
		ThrottleSeconds: 24 * 60 * 60,
		Description:     "某用户 balance < 0 且 |balance| 达到阈值。单个用户欠款到这个量级说明消费远超充值。",
	},
	{
		RuleID:   RiskRuleIDDebtBatch,
		Name:     "单批次欠款超阈值",
		Severity: RiskSeverityHigh,
		// 118 §2.4 触发 2：一次批量回滚产生的欠款总额 >= 1000 元，每批次只告警一次。
		Threshold:       1000,
		ThrottleSeconds: 0,
		Description:     "批量赠送回滚在同一批次内形成的欠款总额达到阈值，直接反映这次操作造成多少损失。",
	},
	{
		RuleID:   RiskRuleIDDebtSystem,
		Name:     "系统总欠款突增",
		Severity: RiskSeverityHigh,
		// 118 §2.4 触发 3：总欠款 24h 增量 >= 5000 元，每 6h 最多一次。
		Threshold:       5000,
		ThrottleSeconds: 6 * 60 * 60,
		Description:     "系统总欠款（Σ|balance<0|）相对 24 小时前的增幅达到阈值，是最快的「出事了」信号。",
	},
}

// RiskRule 是一条风控规则的可配置行（risk_rules）。
//
// 规则行只承载**运营可调的部分**：是否启用、级别、节流、阈值。
// 判定逻辑本身在代码里（service/risk_alert.go），不做可编程规则引擎 ——
// 119 §5.2 已把「可配置规则引擎」划到完整风控（后续），本期只做最小集。
type RiskRule struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// RuleID 是稳定的字符串标识（debt_user / debt_batch / debt_system）。
	// 阈值配置键与告警记录都按它关联，所以它必须有唯一索引且**永不改写**。
	RuleID string `gorm:"column:rule_id;type:varchar(64);not null;default:'';uniqueIndex:uk_risk_rule_rule_id" json:"ruleId"`
	Name   string `gorm:"column:name;type:varchar(128);not null;default:''" json:"name"`
	// Severity 取 high / medium / low（96 §2.1）。
	Severity string `gorm:"column:severity;type:varchar(16);not null;default:'medium'" json:"severity"`
	// Enabled 单条开关。
	//
	// ⚠️ 带 `default:true` 的已知陷阱（本仓 blacklist.go / review_rebate.go 都记过）：
	// GORM 会把**等于默认值**的字段从 INSERT 里省略，所以 `Create(&riskRule{Enabled: false})`
	// 会被静默写成 true。本表种子全部是 true，不受影响；若将来要新建一条停用规则，
	// 必须走「Create 之后 Update enabled=false」两步。
	Enabled bool `gorm:"column:enabled;not null;default:true" json:"enabled"`
	// Threshold 是阈值（金额，元）。种子写入代码默认值，运营可直接改这一列；
	// `risk_rule_<rule_id>_threshold` 系统设置仍然优先（见 service.resolveRiskRuleThreshold）。
	Threshold float64 `gorm:"column:threshold;type:decimal(18,6);not null;default:0" json:"threshold"`
	// ThrottleSeconds 是节流窗口语义：
	//
	//	> 0  窗口内同一 SubjectKey 只告警一次（debt_user 24h、debt_system 6h）
	//	= 0  同一 SubjectKey **永不重复**（debt_batch：一个批次只该告警一次，
	//	     与「回滚结束时报一次」的规格一致，不依赖时间窗）
	//	< 0  不节流（每次命中都写告警，目前没有规则用它）
	ThrottleSeconds int       `gorm:"column:throttle_seconds;not null;default:0" json:"throttleSeconds"`
	Description     string    `gorm:"column:description;type:varchar(255);not null;default:''" json:"description"`
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*RiskRule) TableName() string { return TableNameRiskRule }

// RiskWarning 是一条告警记录（risk_warnings）。
//
// 与 96 §2.3 的 RiskAlert 是同一件事（96 叫 RiskAlert，96 §7 的表名是 risk_warnings；
// 119 §4.2 裁决「命名以 96 号为准」，故实体叫 RiskWarning、表叫 risk_warnings）。
//
// 两条不可协商的性质：
//
//  1. **只告警不拦截**（96 C1）：本表的写入绝不改用户状态、绝不拒绝请求。
//     读它就是「看过」，不读它就是「没看」，不影响任何业务判定。
//  2. **Detail 是证据快照**（96 §2.3 关键段）：写触发时刻的计数、阈值、关联账号，
//     不是「实时查询结果」。告警要能复盘，事后资产变了也必须能看到当时的证据。
type RiskWarning struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// RuleID 关联 risk_rules.rule_id（debt_user / debt_batch / debt_system）。
	RuleID string `gorm:"column:rule_id;type:varchar(64);not null;default:'';index:idx_risk_warning_rule_created,priority:1;index:idx_risk_warning_subject,priority:1" json:"ruleId"`
	// SubjectKey 是节流主体：user:12345 / batch:batch-7f3a / system:。
	//
	// 它必须存在，否则「同一用户 24h 只告警一次」无法判定 ——
	// 靠 Detail 里的 JSON 反查既慢又不可靠（Detail 是给人看的快照，不承诺结构稳定）。
	SubjectKey string `gorm:"column:subject_key;type:varchar(191);not null;default:'';index:idx_risk_warning_subject,priority:2" json:"subjectKey"`
	// Severity 是触发时的级别快照（规则后来被改级也不影响历史记录）。
	Severity string `gorm:"column:severity;type:varchar(16);not null;default:'medium'" json:"severity"`
	// UserID 可能为空：纯批次/系统维度的告警不归属某个用户。
	UserID *int32 `gorm:"column:user_id;type:int(11);index:idx_risk_warning_user" json:"userId,omitempty"`
	// IP 是证据里的来源 IP（欠款三条规则目前不填，为后续行为规则预留）。
	IP string `gorm:"column:ip;type:varchar(64);not null;default:''" json:"ip"`
	// Detail 是 JSON 证据快照（96 §2.3 / 121 §4.6 里叫 Payload）。
	Detail string `gorm:"column:detail;type:text" json:"detail"`
	// Notified 表示**企业微信是否已推送成功**。
	// false 不等于「没告警」—— 告警记录本身已经落库（96 §2.4：节流不等于聚合）。
	Notified   bool       `gorm:"column:notified;not null;default:false" json:"notified"`
	NotifiedAt *time.Time `gorm:"column:notified_at" json:"notifiedAt,omitempty"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime;index:idx_risk_warning_rule_created,priority:2;index:idx_risk_warning_subject,priority:3;index:idx_risk_warning_created,priority:1" json:"createdAt"`
}

func (*RiskWarning) TableName() string { return TableNameRiskWarning }
