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

// 规则 rule_id 常量。
//
// 命名刻意与 96 §2.2 的配置键风格对齐（`risk_rule_<rule_id>_threshold`）。
// 欠款三条（118 号决策）用短名；行为规则沿用 96 §2.1 的原始描述名
// （`multi_account_ip`），因为它们是 96/97 号文档里已有的公开标识，
// 改名会让「文档—代码」对照失效，而对照正是后续 17 条规则派工的依据。
const (
	RiskRuleIDDebtUser   = "debt_user"
	RiskRuleIDDebtBatch  = "debt_batch"
	RiskRuleIDDebtSystem = "debt_system"
	// RiskRuleIDMultiAccountIP 是 96 §2.1 的 R1「同 IP 多账号」——
	// 18 条行为规则里**第一条落地**的（范式见 service/risk_alert.go
	// collectMultiAccountIPCandidates 的注释）。
	//
	// ⚠️ 它的一条老注释曾经把 multi_account_ip 写成「命名反例」（说明为什么
	// debt_* 用短名）。现在它已经是**真规则**，那条注释已随之改写 ——
	// 否则后面的人 grep 到的仍是「这是个反例」，会误判成未实现。
	RiskRuleIDMultiAccountIP = "multi_account_ip"
	// RiskRuleIDTicketFlood 是 96 §2.1 的 R8「工单刷屏」：
	// 单用户 24h 内建单 ≥N 张（N 默认 5）。
	//
	// 数据源：tickets 主表（user_id + created_at）。
	//
	// ⚠️ 索引实情（实测 EXPLAIN，别被「两列都有索引」误导）：
	// 只有 `idx_tickets_user_id` 与 `idx_tickets_created_at` 两条**独立单列**索引，
	// **没有 (user_id, created_at) 复合索引**。而且窗口过滤用的是
	// `datetime(created_at,'+8 hours')`（时区换算，见 risk_alert.go 的
	// riskWindowColumnExpr），**函数调用会让 created_at 索引失效**，EXPLAIN 实际走
	// `SEARCH tickets USING INDEX idx_tickets_user_id (user_id>?)`。
	// 也就是说这个查询**没有被 created_at 索引覆盖**，只是靠 user_id 索引缩小了范围。
	// 当前规模（实测 tickets 0 行）可接受；若 tickets 长大后本规则变慢，
	// 正确处理是**加复合索引**，而不是去掉时区换算（去掉会错 8 小时）。
	//
	// 只数「建单」，不数 ticket_messages —— 回复不是新建工单。
	RiskRuleIDTicketFlood = "ticket_flood"
	// RiskRuleIDAbnormalRegisterPattern 是 96 §2.1 的 R11「注册名高度模板化」：
	// 同一时间窗内出现 ≥N 个「同前缀 + 连续递增数字」的用户名。
	//
	// 数据源：users.username + users.created_at。
	// ⚠️ 这两列**都没有索引**（见 service.collectAbnormalRegisterPatternCandidates
	// 的性能说明），因此按时间窗先过滤缩小候选集是必要的。
	RiskRuleIDAbnormalRegisterPattern = "abnormal_register_pattern"
	// RiskRuleIDTokenAbuse 是 96 §2.1 的 R12「access token 滥用」。
	//
	// 数据源：access_tokens 的现成字段（**不新建调用日志表**）。
	// 见 service.collectTokenAbuseCandidates 的注释：用 billing_records 聚合是
	// 方向性错误（结果为空不记账，而滥用恰好高发于「查了没查到」）。
	RiskRuleIDTokenAbuse = "token_abuse"
	// RiskRuleIDOffhoursActivity 是 96 §2.1 的 R16「凌晨作息异常」。
	//
	// 数据源：risk_warnings **自身历史**（不新建快照表）。
	// 见 service.collectOffhoursActivityCandidates 的注释。
	RiskRuleIDOffhoursActivity = "offhours_activity"
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

// DefaultRiskBehaviorRules 是 96 §2.1 那批**行为**规则的代码默认值。
//
// 与 DefaultRiskDebtRules 分开两个切片，不是审美选择：
//
//  1. 两者的开关默认相反 —— 欠款规则默认 true（118 §2.9：欠款不设上限的唯一安全阀），
//     行为规则按 96 §7 默认也 true 但**误报代价完全不同**（行为规则靠阈值+窗口压制误报，
//     欠款规则靠金额）。混在一个切片里，将来「行为规则默认关」的决策会连带关掉安全阀。
//  2. 测试与运维都需要一个「只要欠款三条」的稳定集合（既有用例断言 len==3）。
//
// 当前只有 R1（multi_account_ip）。R2–R18 落地时往这里追加，判定函数各写一个
// collectXxxCandidates，其余全复用（范式见 service/risk_alert.go 的同名注释）。
var DefaultRiskBehaviorRules = []RiskRuleDefault{
	{
		RuleID:   RiskRuleIDMultiAccountIP,
		Name:     "同 IP 多账号",
		Severity: RiskSeverityMedium,
		// 96 §2.1 R1：同一 IP 在 24h 内出现 ≥N 个不同 user_id 登录成功，N 默认 5。
		Threshold: 5,
		// 0 = 同一 SubjectKey 永不重复。R1 的 SubjectKey 已经把**窗口起点**编进去
		// （`multi_account_ip:<ip>:<窗口起点>`），所以「同一窗口内只一条、下一个窗口
		// 再一条」是由 key 本身保证的，不需要额外的窗口节流 —— 再加时间窗反而会让
		// 『隔一个窗口后再告警』在边界上失效（两条判据互相打架）。
		ThrottleSeconds: 0,
		Description:     "同一 IP 在 24 小时窗口内登录了达到阈值的不同账号数。常见于共享出口（校园网/公司 NAT）与养号群体，只告警不处置。",
	},
	{
		RuleID:   RiskRuleIDTicketFlood,
		Name:     "工单刷屏",
		Severity: RiskSeverityLow,
		// 96 §2.1 R8：单用户 24h 内建单 ≥N 张，N 默认 5，每用户 24h 一次。
		Threshold:       5,
		ThrottleSeconds: 24 * 60 * 60,
		Description:     "单用户在 24 小时窗口内新建工单数达到阈值。多为反复提交同一问题或滥用客服通道，只告警不处置。",
	},
	{
		RuleID:   RiskRuleIDAbnormalRegisterPattern,
		Name:     "注册名高度模板化",
		Severity: RiskSeverityMedium,
		// 96 §2.1 R11：同前缀 + 递增数字的用户名 ≥N 个，N 默认 3。
		Threshold:       3,
		ThrottleSeconds: 24 * 60 * 60,
		Description:     "同一自然日窗口内出现达到阈值的「同前缀（≥3 字符）+ 连续递增数字」用户名，常见于批量注册养号，只告警不处置。",
	},
	{
		RuleID:   RiskRuleIDTokenAbuse,
		Name:     "access token 滥用",
		Severity: RiskSeverityMedium,
		// 96 §2.1 R12：单个 access_token 调用量 > 其配额 3 倍（或被多 IP 并发使用）。
		//
		// 本实现把它落成两条子判据（见 collectTokenAbuseCandidates）：
		//   ① 配额消耗进度 TotalAmountUsed/TotalQuotaLimit ≥ 阈值（默认 3.0 = 300%）
		//   ② 多 IP 漂移：同一 token 在窗口内被 ≥ 阈值个不同 IP 使用
		// 两者取「或」——任一条命中即告警。阈值默认 3 对两条都成立（3 倍配额 / 3 个 IP）。
		Threshold:       3,
		ThrottleSeconds: 24 * 60 * 60,
		Description:     "单个 access token 的配额消耗达到配额数倍，或在同一窗口内被多个不同 IP 使用（疑似令牌外泄/共享）。只告警不处置。",
	},
	{
		RuleID:   RiskRuleIDOffhoursActivity,
		Name:     "凌晨作息异常",
		Severity: RiskSeverityLow,
		// 96 §2.1 R16：凌晨 2-5 点答题量占全天 >80% 且连续 3 天。
		//
		// ⚠️ 本规则的「连续 3 天」用的是**告警历史**，不是答题日志：
		// 每次判定若当天凌晨活动占比超阈值就写一条（SubjectKey 编日期），
		// 「连续 3 天」= 近 3 天能找到 3 条不同日期的记录。
		//
		// ⇒ **新部署的环境首 3 天不会产生告警**（没有历史就没有连续证据）。
		// 这不是缺陷，是设计取舍：宁可延迟发现，也不要因为「第 1 天就报警」而误报。
		// 阈值 3 = 连续 3 天。
		Threshold:       3,
		ThrottleSeconds: 24 * 60 * 60,
		Description:     "连续 3 个自然日出现凌晨时段（默认北京时间 02:00-05:00，可配）活动占比超过阈值。注意：本规则需要至少 3 天历史数据，新部署环境前 3 天不会产生告警。",
	},
}

// DefaultRiskRules 返回**全部**代码默认规则（欠款 + 行为）。
//
// 判定、种子、兜底三处都必须走这个函数而不是直接遍历某一个切片：
// 漏掉任何一条 = 少一道闸，而且不会有任何报错（规则就是静默不生效）。
func DefaultRiskRules() []RiskRuleDefault {
	all := make([]RiskRuleDefault, 0, len(DefaultRiskDebtRules)+len(DefaultRiskBehaviorRules))
	all = append(all, DefaultRiskDebtRules...)
	all = append(all, DefaultRiskBehaviorRules...)
	return all
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
	// SubjectKey 是节流主体：user:12345 / batch:batch-7f3a / system:debt /
	// multi_account_ip:<ip>:<窗口起点>。
	//
	// 它必须存在，否则「同一用户 24h 只告警一次」无法判定 ——
	// 靠 Detail 里的 JSON 反查既慢又不可靠（Detail 是给人看的快照，不承诺结构稳定）。
	//
	// ⚠️ **已知并发缺口（如实记录，本轮决策为「不修」，理由见下）**：
	// 节流用的是「先查 (rule_id, subject_key) 再插入」。缺口本身已由
	// service.insertRiskWarningIfAbsent 用单条 `INSERT ... SELECT ... WHERE NOT EXISTS`
	// 补上（判重与写入同一 statement，不依赖隔离级别）；这里记录的是
	// **为什么没有加唯一索引这层额外兜底**。
	//
	// 不能加 `UNIQUE(rule_id, subject_key)` 的原因（实测，不是推断）：
	// 加上之后 TestRiskEvaluateDebtAlertsUserRuleThresholdAndThrottle 立刻红
	// （"after the throttle window the same users must alert again, created=0"）——
	// 窗口节流的规则（debt_user 24h / debt_system 6h）需要在**下一个窗口**
	// 为**同一 SubjectKey** 再写一行，唯一索引让它永远只能有一行，
	// 直接废掉「隔一个窗口后再告警」。
	//
	// 这是**数据模型层面的互斥**，不是实现问题：要让两者兼得，必须把
	// 「窗口标识」独立成一列（表结构变更 + 迁移兼容 + 存量回填）。评估后不做，因为：
	//   - 语义上原子性已由单条 INSERT 拿到，唯一索引只是多一层兜底，不是唯一正确性来源；
	//   - 唯一索引方案真正的代价不是变更本身，而是它会**把「同一 SubjectKey 在不同窗口
	//     各写一行」这个正确行为判成违规**。任何绕过它的写法（例如写时给 subject_key
	//     拼时间后缀）都会污染 SubjectKey 的语义 —— 而 SubjectKey 是排障时唯一的稳定键。
	// 若将来确需唯一约束，正确方向是加 `subject_window_start` 列并以其入索引。
	//
	// 影响面（供决策参考）：并发窗口极窄（同一瞬间两个 worker 判定同一主体），
	// 后果是**多一条重复告警**（噪音），不是漏报，也不影响任何处置（风控不处置）。
	// 回归钉：TestRiskWarningDedupIsAtomicAcrossAllRules（四条规则 × 16 并发）。
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
