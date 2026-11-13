package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 风控告警（仅告警，绝不拦截）—— 96-风控管理仅告警与用户画像-设计.md。
//
// 本文件只做「最小集」（119 §5.2 决策 C）：
//   - 风控专属 webhook 配置（96 C2/C3 的落地）；
//   - 3 条欠款告警规则（118 §2.4）；
//   - 告警记录写入 risk_warnings + 节流推送。
//
// ## 三条不可协商的约束（96 §0）
//
//	C1 只告警不拦截：本文件**不碰** users.status / 不拒绝请求 / 不改任何用户状态。
//	C2 独立 webhook：只读 wecom_risk_* 三个键，**绝不**复用 wecom_ticket_webhook_url。
//	C3 两个输入框：即使两个 URL 填成同一个值，也是两条独立记录、独立 clear 语义。
//
// ⚠️ C2 是最容易做错的地方（96 §0「C3 的实现含义」给了正反两段代码）。
// 唯一正确的写法是「风控用自己的 key 前缀」，任何形态的 `if risk == "" { risk = ticket }`
// 都是错的：用户明确禁止隐式复用。风控 webhook 未配置时的正确行为是
// **只写 risk_warnings、不推送**，而不是借工单的通道发出去。

const (
	// RiskWecomEnabledSettingKey 是否启用风控企业微信通知。
	RiskWecomEnabledSettingKey = "wecom_risk_notifications_enabled"
	// RiskWecomWebhookURLSettingKey 是**风控专用** webhook，与工单那条完全独立。
	RiskWecomWebhookURLSettingKey = "wecom_risk_webhook_url"
	// RiskWecomMentionedListSettingKey 是风控通知的 @成员 ID 列表。
	RiskWecomMentionedListSettingKey = "wecom_risk_mentioned_list"
)

// riskSystemDebtBaselineAtKey / riskSystemDebtBaselineValueKey 是「系统总欠款 24h 增量」
// 的基线（118 §2.4 触发 3）。
//
// 为什么不新建快照表：121 §4 只批准了 risk_rules 与 risk_warnings 两张表，
// 而基线是**一个标量**，放进 system_settings 与迁移标记同类（都是运行期状态）。
// 语义：上次轮换时（至少 24h 前）的总欠款，24h 轮换一次。
//
// ⚠️ 首次运行没有基线 → **不告警而是记基线**。这是有意的 fail-safe：
// 没有对比基准时任何「增量」都是凭空捏造的，误报会立刻摧毁告警可信度。
var (
	riskSystemDebtBaselineAtKey    = "risk_system_debt_baseline_at"
	riskSystemDebtBaselineValueKey = "risk_system_debt_baseline_value"
)

// 三条欠款规则依赖的账单形态（118 §5.2）：批量回滚分两笔记账，
// 只有 principal 那一笔能把余额扣成负数（赠金桶下限 0，扣不出欠款）。
const (
	riskBatchReversalBillingType      = "admin_grant_reversal"
	riskBatchReversalPrincipalPrefix  = "admin_grant_reversal_principal_"
	riskBatchReversalInspectionWindow = 24 * time.Hour
	riskIndebtedUserScanLimit         = 500
	riskSystemDebtWindow              = 24 * time.Hour
	riskWarningListPageSizeDefault    = 20
	riskWarningListPageSizeMax        = 100
)

// RiskWecomSettings 是风控 webhook 配置的**响应形态**（96 §1.1 的可复用契约）。
//
// WebhookURL **响应里恒为空**：它是 credential-bearing 的凭据，
// 与 ticket_settings.go 的做法一致（空白更新 = 保持原值）。
type RiskWecomSettings struct {
	Enabled       bool     `json:"enabled"`
	Configured    bool     `json:"configured"`
	WebhookURL    string   `json:"webhookUrl"`
	Preview       string   `json:"preview"`
	MentionedList []string `json:"mentionedList"`
}

// UpdateRiskWecomSettingsInput 是写入风控 webhook 配置的入参。
type UpdateRiskWecomSettingsInput struct {
	Enabled       *bool    `json:"enabled"`
	WebhookURL    string   `json:"webhookUrl"`
	ClearWebhook  bool     `json:"clearWebhook"`
	MentionedList []string `json:"mentionedList"`
}

// RiskWecomConfig 是风控发信配置（服务内部形态，含真实 URL）。
//
// 与 TicketWecomConfig 是**两个独立类型**而不是同一个类型加 scope 参数，
// 是因为后者允许调用点误传 scope；类型分开则「把 ticket 配置传给风控发送器」
// 在同包里也不可能 —— 两个类型之间没有隐式转换。
// 96 §4.4 建议的 WecomScope 方案在同包内没有这个保护。
//
// ⚠️ 类型不叫 RiskAlertConfig：那个名字被**函数** RiskAlertConfig(db) 占用
// （Go 不允许类型与函数同名），而函数名是任务契约固定的。
type RiskWecomConfig struct {
	Enabled       bool
	WebhookURL    string
	MentionedList []string
}

// RiskAlertsDisabledReasons 是「为什么没推送」的枚举（供告警记录与日志复用）。
const (
	riskNotifySent             = "sent"
	riskNotifyDisabled         = "notification_disabled"
	riskNotifyWebhookEmpty     = "webhook_not_configured"
	riskNotifyThrottled        = "throttled"
	riskNotifySendFailed       = "send_failed"
	riskNotifyNoDatabase       = "database_unavailable"
	riskNotifyNoWarningsToSend = "no_warning"
)

// RiskWarningFilter 是告警列表的查询条件（管理页只读）。
type RiskWarningFilter struct {
	RuleID string
	UserID int32
	// Notified 为 nil 表示不筛选；裸 bool 会把「不筛选」退化成「只看未推送」。
	Notified    *bool
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	Page        int
	PageSize    int
}

// RiskWarningListResult 是告警列表的响应形态。
type RiskWarningListResult struct {
	Data     []entity.RiskWarning `json:"data"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

// RiskAlertEvaluation 是一次判定的结果摘要（供定时任务打日志/测试断言）。
type RiskAlertEvaluation struct {
	// Created 是本次**新建**的告警行数（被节流不算创建失败）。
	Created int
	// Pushed 是本次成功推送到企业微信的条数。
	Pushed int
	// Suppressed 是因节流/未配置而只落库未推送的条数。
	Suppressed int
	// RuleIDs 是本次真正命中的规则（按写入顺序去重）。
	RuleIDs []string
}

// ErrInvalidRiskWecomWebhook 是风控 webhook 的校验错误。
//
// 文案刻意写明「风控」而不是「工单」：两个输入框在同一页面并排，
// 报错如果不指明是哪一个，管理员会把风控的错怪到工单配置上（96 §4.2 的提示语同源）。
var ErrInvalidRiskWecomWebhook = fmt.Errorf("企业微信风控 Webhook 必须是 HTTPS 群机器人地址")

// RiskAlertConfig 读取风控发信配置。
//
// ⚠️ 这里**只读 wecom_risk_* 三个键**。绝不读 wecom_ticket_*（96 C2）。
// 风控 webhook 为空时返回的空 URL 就是「不推送」的唯一依据 ——
// 由调用方（evaluate + notifyRiskWebhook）判断，本函数不做任何跨 scope 回退。
func RiskAlertConfig(db *gorm.DB) (RiskWecomConfig, error) {
	if db == nil {
		return RiskWecomConfig{}, fmt.Errorf("数据库未初始化")
	}
	var settings []entity.SystemSetting
	keys := []interface{}{
		RiskWecomEnabledSettingKey,
		RiskWecomWebhookURLSettingKey,
		RiskWecomMentionedListSettingKey,
	}
	if err := db.Where(clause.IN{Column: clause.Column{Name: "key"}, Values: keys}).Find(&settings).Error; err != nil {
		return RiskWecomConfig{}, err
	}
	config := RiskWecomConfig{}
	for _, setting := range settings {
		switch setting.Key {
		case RiskWecomEnabledSettingKey:
			config.Enabled = strings.EqualFold(strings.TrimSpace(setting.Value), "true") || strings.TrimSpace(setting.Value) == "1"
		case RiskWecomWebhookURLSettingKey:
			config.WebhookURL = strings.TrimSpace(setting.Value)
		case RiskWecomMentionedListSettingKey:
			config.MentionedList = normalizeMentionedList(setting.Value)
		}
	}
	return config, nil
}

// GetRiskWecomSettings 返回脱敏后的风控 webhook 配置（响应形态）。
func GetRiskWecomSettings(db *gorm.DB) (RiskWecomSettings, error) {
	config, err := RiskAlertConfig(db)
	if err != nil {
		return RiskWecomSettings{}, err
	}
	return RiskWecomSettings{
		Enabled:    config.Enabled,
		Configured: strings.TrimSpace(config.WebhookURL) != "",
		// 恒为空：风控 webhook 是凭据，绝不回显。
		WebhookURL:    "",
		Preview:       maskTicketWebhookURL(config.WebhookURL),
		MentionedList: config.MentionedList,
	}, nil
}

// UpdateRiskWecomSettings 写入风控 webhook 三键。
//
// 三条语义（与 ticket_settings.go 对齐，但**写的是风控自己的键**）：
//   - 空白 或 SecretMask → 保持原值（前端表单不回显凭据，必须支持「只改开关」）；
//   - ClearWebhook=true → 清空，且**只清风控那条**，工单那条一个字节都不动；
//   - 两个 URL 填成相同值 → 写入两条独立记录，互不影响（96 C3 判据 3）。
func UpdateRiskWecomSettings(db *gorm.DB, input UpdateRiskWecomSettingsInput) (RiskWecomSettings, error) {
	if db == nil {
		return RiskWecomSettings{}, fmt.Errorf("数据库未初始化")
	}
	current, err := RiskAlertConfig(db)
	if err != nil {
		return RiskWecomSettings{}, err
	}
	webhook := current.WebhookURL
	switch {
	case input.ClearWebhook:
		webhook = ""
	case strings.TrimSpace(input.WebhookURL) != "" && input.WebhookURL != SecretMask:
		webhook = strings.TrimSpace(input.WebhookURL)
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if enabled {
		if err := validateRiskWecomWebhookURL(webhook); err != nil {
			return RiskWecomSettings{}, err
		}
	}
	mentioned := current.MentionedList
	if input.MentionedList != nil {
		mentioned = normalizeTicketMentionedList(input.MentionedList)
	}
	encoded, err := json.Marshal(mentioned)
	if err != nil {
		return RiskWecomSettings{}, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		items := []struct{ key, value, remark string }{
			{RiskWecomEnabledSettingKey, map[bool]string{true: "true", false: "false"}[enabled], "是否启用企业微信风控告警通知"},
			{RiskWecomWebhookURLSettingKey, webhook, "企业微信风控告警群机器人 Webhook（独立于工单，绝不复用）"},
			{RiskWecomMentionedListSettingKey, string(encoded), "企业微信风控告警 @成员 ID"},
		}
		for _, item := range items {
			if err := saveSystemSettingValue(tx, item.key, item.value, item.remark); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return RiskWecomSettings{}, err
	}
	return GetRiskWecomSettings(db)
}

// validateRiskWecomWebhookURL 复用群机器人地址的严格校验（HTTPS + 固定 host/path + key 非空）。
//
// 空串返回 nil：允许「先关闭再清空」，此时不推送而不是报错。
func validateRiskWecomWebhookURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := validateTicketWecomWebhookURL(raw); err != nil {
		return ErrInvalidRiskWecomWebhook
	}
	return nil
}

// RiskWebhookPreview 返回脱敏预览（供测试与控件复用）。
func RiskWebhookPreview(raw string) string { return maskTicketWebhookURL(raw) }

// notifyRiskWebhook 推送一条风控告警。
//
// 三个门，顺序固定，任何一个不满足就**只落库不推送**：
//  1. 没有任何告警行 → 什么都不做；
//  2. 配置未启用 / webhook 为空 → 不推送（**绝不回退到工单 webhook**）；
//  3. 节流窗口内有已推送记录 → 不推送。
//
// 返回 (pushed, reason, error)。reason 是给测试与日志看的多态区分：
// 「未配置」与「被节流」在界面上都表现为「没推送」，但下一步动作完全相反
// （前者去配 webhook、后者什么都不用做）。
func notifyRiskWebhook(ctx context.Context, db *gorm.DB, warning *entity.RiskWarning, now time.Time) (bool, string, error) {
	if db == nil || warning == nil {
		return false, riskNotifyNoDatabase, nil
	}
	config, err := RiskAlertConfig(db)
	if err != nil {
		return false, riskNotifySendFailed, err
	}
	// ⚠️ 判定顺序固定：「URL 为空」先于「开关关闭」。
	//
	// 理由：URL 为空时**无论开关怎么设都发不出去**，而两件事的可操作结论不同 ——
	//   - URL 为空 → 去配置风控 webhook（96 C2 的正题）；
	//   - 开关关闭 → 去打开开关。
	// 把「什么都没配」报成 notification_disabled 会让人去点开关，点完还是不发 ——
	// 这是「诊断字段描述症状而不是原因」的典型错法（本仓 t143 已吃过一次）。
	if strings.TrimSpace(config.WebhookURL) == "" {
		// ⚠️ 96 C2：这里**不能**去读 wecom_ticket_webhook_url。
		// 风控没配 webhook 就是不推送 —— 借工单的通道会把运维群和工单群混在一起，
		// 而且是用户明确禁止的隐式复用。
		return false, riskNotifyWebhookEmpty, nil
	}
	if !config.Enabled {
		return false, riskNotifyDisabled, nil
	}
	if err := validateRiskWecomWebhookURL(config.WebhookURL); err != nil {
		return false, riskNotifySendFailed, err
	}
	content, err := buildRiskWarningMarkdown(*warning)
	if err != nil {
		return false, riskNotifySendFailed, err
	}
	if err := riskWebhookSender(ctx, config, content, config.MentionedList); err != nil {
		return false, riskNotifySendFailed, err
	}
	notifiedAt := now
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).Where("id = ?", warning.ID).
		Updates(map[string]interface{}{"notified": true, "notified_at": notifiedAt}).Error; err != nil {
		return true, riskNotifySent, err
	}
	warning.Notified = true
	warning.NotifiedAt = &notifiedAt
	return true, riskNotifySent, nil
}

// riskWebhookSender 是可替换的发信钩子。
//
// 抽成变量是为了让测试能断言「未配置风控 webhook 时根本没有发起 HTTP」——
// 只断言 Notified=false 抓不到「回退到工单 webhook 但发送失败」这类实现错误。
var riskWebhookSender = func(ctx context.Context, config RiskWecomConfig, content string, mentioned []string) error {
	return SendRiskTestWebhook(ctx, config, content, mentioned)
}

// SendRiskTestWebhook 用**传入的风控配置**发一条消息（测试按钮与真实告警共用）。
//
// ⚠️ 本函数**只信任传入的 config.WebhookURL**。它不读任何系统设置，
// 因此也不可能回退到工单 webhook —— 这是 96 C2 在发信层的第二道保险：
// 即使调用方误把工单配置传进来，那也只是「发到工单群」，
// 而不是「风控静默借用工单通道」。配置来源只由 RiskAlertConfig(db) 决定。
func SendRiskTestWebhook(ctx context.Context, config RiskWecomConfig, content string, mentioned []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(config.WebhookURL) == "" {
		return ErrInvalidRiskWecomWebhook
	}
	if err := validateRiskWecomWebhookURL(config.WebhookURL); err != nil {
		return err
	}
	return SendTicketWebhook(ctx, TicketWecomConfig{
		Enabled:       true,
		WebhookURL:    config.WebhookURL,
		MentionedList: mentioned,
	}, content, mentioned)
}

// buildRiskWarningMarkdown 生成风控告警消息（96 §4.3 的格式）。
//
// 消息里的跳转链接指向**风控告警详情**，不是工单详情 ——
// 这是 96 §4.3 强调「推送目标配置要分开，消息里的链接语义也要分开」的落地。
// 目前没有前端路由文件可引用，链接用相对路径常量，前端接入时只改这一处。
func buildRiskWarningMarkdown(warning entity.RiskWarning) (string, error) {
	evidence := map[string]any{}
	if raw := strings.TrimSpace(warning.Detail); raw != "" {
		if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
			// 证据快照坏了不能让告警发不出去 —— 告警本身比证据格式重要。
			evidence = map[string]any{"unparsableDetail": raw}
		}
	}
	title := riskRuleDisplayName(warning.RuleID)
	severity := map[string]string{
		entity.RiskSeverityHigh:   "高",
		entity.RiskSeverityMedium: "中",
		entity.RiskSeverityLow:    "低",
	}[warning.Severity]
	if severity == "" {
		severity = warning.Severity
	}
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "## 【风控告警】%s · %s\n", severity, escapeTicketMarkdown(title))
	if warning.UserID != nil {
		fmt.Fprintf(builder, "> **用户：** #%d\n", *warning.UserID)
	}
	if key := strings.TrimSpace(warning.SubjectKey); key != "" {
		fmt.Fprintf(builder, "> **主体：** %s\n", escapeTicketMarkdown(key))
	}
	if threshold, ok := evidence["threshold"]; ok {
		fmt.Fprintf(builder, "> **阈值：** %v\n", threshold)
	}
	if value, ok := evidence["value"]; ok {
		fmt.Fprintf(builder, "> **实测：** %v\n", value)
	}
	fmt.Fprintf(builder, "> **时间：** %s\n", warning.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(builder, "\n→ 查看告警: %s%d", riskAlertDetailPathPrefix, warning.ID)
	return builder.String(), nil
}

// riskAlertDetailPathPrefix 是风控告警详情的相对路径前缀（96 §4.3 的 `{BASE_URL}/admin/risk/alerts/{id}`）。
//
// 与工单详情 `/admin/tickets/{id}` 是两条不同的路径 —— 即使两个 webhook 填了同一个群，
// 点进去也不会串到工单页。
const riskAlertDetailPathPrefix = "/admin/risk/alerts/"

// riskRuleDisplayName 返回规则的中文名（告警消息与列表都用它）。
func riskRuleDisplayName(ruleID string) string {
	for _, item := range entity.DefaultRiskDebtRules {
		if item.RuleID == ruleID {
			return item.Name
		}
	}
	if strings.TrimSpace(ruleID) == "" {
		return "未知规则"
	}
	return ruleID
}

// riskRuleThresholdSettingKey 是规则阈值的系统设置键（96 §2.2 的 `risk_rule_<id>_threshold`）。
func riskRuleThresholdSettingKey(ruleID string) string {
	return "risk_rule_" + ruleID + "_threshold"
}

// riskRuleThreshold 解析某条规则的有效阈值。
//
// 三层兜底，顺序固定（96 §2.2：默认值写在代码里，DB 缺 key ≠ 阈值 0）：
//
//  1. `risk_rule_<id>_threshold` 系统设置（运营最常调的地方）；
//  2. risk_rules.threshold 列（表里改）；
//  3. entity.DefaultRiskDebtRules 的代码默认值。
//
// ⚠️ 三层里任何一层出现「解析不出正数」都继续往下走。
// 一旦把无效值当成 0，`|balance| >= 0` 会让**每个**用户都告警 —— 这是本文件里
// 最严重的失效形态（不是漏报而是刷屏）。
func riskRuleThreshold(db *gorm.DB, rule entity.RiskRule, fallback float64) float64 {
	if db != nil {
		raw := getSystemSettingStringWithDefault(db, riskRuleThresholdSettingKey(rule.RuleID), "")
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && value > 0 {
			return value
		}
	}
	if rule.Threshold > 0 {
		return rule.Threshold
	}
	return fallback
}

// loadRiskRules 读取规则行并按默认值补齐缺失的规则。
//
// 缺失规则（表还没建/行被删）**不能**被静默跳过：欠款告警是「欠款不设上限」的唯一
// 安全阀（118 §2.9），少一条规则等于少一道闸。所以这里用代码默认值补齐，
// 并把「行不存在」当成「用默认值、按默认启用」处理。
func loadRiskRules(db *gorm.DB) ([]entity.RiskRule, error) {
	rows := make([]entity.RiskRule, 0, len(entity.DefaultRiskDebtRules))
	if db != nil && db.Migrator().HasTable(entity.TableNameRiskRule) {
		var stored []entity.RiskRule
		if err := db.Find(&stored).Error; err != nil {
			return nil, err
		}
		rows = append(rows, stored...)
	}
	byID := make(map[string]entity.RiskRule, len(rows))
	for _, row := range rows {
		byID[row.RuleID] = row
	}
	result := make([]entity.RiskRule, 0, len(entity.DefaultRiskDebtRules))
	for _, item := range entity.DefaultRiskDebtRules {
		if row, ok := byID[item.RuleID]; ok {
			result = append(result, row)
			continue
		}
		result = append(result, entity.RiskRule{
			RuleID:          item.RuleID,
			Name:            item.Name,
			Severity:        item.Severity,
			Enabled:         true,
			Threshold:       item.Threshold,
			ThrottleSeconds: item.ThrottleSeconds,
			Description:     item.Description,
		})
	}
	return result, nil
}

// EvaluateDebtAlerts 执行 3 条欠款规则的判定，写 risk_warnings，并按风控 webhook 推送。
//
// 96 C1（只告警不拦截）在本函数里体现为：**只 INSERT risk_warnings + 发 webhook**，
// 不改 users、不拒绝请求、不动黑名单。
//
// 三条规则（118 §2.4）：
//
//  1. debt_user   单用户 |balance| >= 阈值（默认 100），同用户 24h 一次；
//  2. debt_batch  单批次回滚产生的欠款 >= 阈值（默认 1000），每批次一次；
//  3. debt_system 系统总欠款相对 24h 基线增量 >= 阈值（默认 5000），6h 一次。
//
// 返回的 error 只表示**基础设施故障**（读库失败），
// 单条告警的推送失败只记 SysWarn —— 推送失败不能让整轮判定中断，
// 更不能让「已经写好的告警行」被事务回滚（告警本身不该因外部通道挂掉而丢）。
func EvaluateDebtAlerts(db *gorm.DB, now time.Time) (RiskAlertEvaluation, error) {
	result := RiskAlertEvaluation{}
	if db == nil {
		return result, fmt.Errorf("数据库未初始化")
	}
	if now.IsZero() {
		now = time.Now()
	}
	rules, err := loadRiskRules(db)
	if err != nil {
		return result, err
	}
	enabledRules := make(map[string]entity.RiskRule, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		enabledRules[rule.RuleID] = rule
	}
	if len(enabledRules) == 0 {
		return result, nil
	}

	candidates := make([]riskWarningCandidate, 0, riskIndebtedUserScanLimit+len(entity.DefaultRiskDebtRules))
	if rule, ok := enabledRules[entity.RiskRuleIDDebtUser]; ok {
		found, scanErr := collectUserDebtCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDDebtBatch]; ok {
		found, scanErr := collectBatchDebtCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDDebtSystem]; ok {
		found, scanErr := collectSystemDebtCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}

	seenRules := map[string]struct{}{}
	for _, candidate := range candidates {
		created, warning, createErr := createRiskWarningIfNeeded(db, candidate, now)
		if createErr != nil {
			return result, createErr
		}
		if created {
			result.Created++
			if _, ok := seenRules[candidate.Rule.RuleID]; !ok {
				seenRules[candidate.Rule.RuleID] = struct{}{}
				result.RuleIDs = append(result.RuleIDs, candidate.Rule.RuleID)
			}
		}
		target := warning
		if !created {
			// 未新建行时也要尝试推送吗？不。未新建意味着「同一事件已经记过」，
			// 再推一次就是重复告警（118 §2.7 例 5 明确：24h 内不重复告警）。
			result.Suppressed++
			continue
		}
		pushed, reason, notifyErr := notifyRiskWebhook(context.Background(), db, target, now)
		if notifyErr != nil {
			logger.SysWarn(fmt.Sprintf("risk_alert_notify_failed ruleId=%s warningId=%d reason=%s error=%v",
				candidate.Rule.RuleID, target.ID, reason, notifyErr))
		}
		if pushed {
			result.Pushed++
		} else {
			result.Suppressed++
		}
	}
	return result, nil
}

// riskWarningCandidate 是一条待判定的命中（已越过阈值，还需过创建节流）。
type riskWarningCandidate struct {
	Rule     entity.RiskRule
	Subject  string
	UserID   *int32
	Severity string
	Detail   string
}

// createRiskWarningIfNeeded 按规则的节流语义决定是否新建告警行。
//
// 规则语义（entity.RiskRule.ThrottleSeconds 的注释）：
//   - > 0：窗口内已有同一 SubjectKey 的告警 → 不新建（同一事件的重复观察不重复留痕）；
//   - = 0：同一个 SubjectKey **永远只建一行**（118 §2.4 触发 2「每批次只告警一次」）；
//   - < 0：每次都新建。
//
// 96 §2.4 说的「每次命中都写 RiskAlert 行（不丢证据）」与本处的「窗口内不重复建行」
// 并不冲突：一次**命中**指的是「一个此前未告警的越界事件」。同一用户在 24h 内余额
// 一直是 -150，那是**一次**欠款事件被多次观察到，而不是 24 次新命中。
// 若按其字面「每次观察都写一行」，3 条规则 × 每 6h 一轮 × 全部欠款用户会以
// 每天 4 倍的速率灌表，且复盘价值为零（Detail 完全一样）。这个取舍写在这里，
// 便于后续按需改成「每次都写 + 只节流推送」。
func createRiskWarningIfNeeded(db *gorm.DB, candidate riskWarningCandidate, now time.Time) (bool, *entity.RiskWarning, error) {
	// ⚠️ 跨 model 复用同一个 *gorm.DB 句柄必须先 Session(NewDB:true)：
	// 否则 Statement 里残留的 model/schema 会让 Create 用错 StructField
	// （本仓 BL-3 出过 reflect: Field index out of range 的 panic）。
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
		Where("rule_id = ? AND subject_key = ?", candidate.Rule.RuleID, candidate.Subject)
	if candidate.Rule.ThrottleSeconds > 0 {
		query = query.Where("created_at > ?", now.Add(-time.Duration(candidate.Rule.ThrottleSeconds)*time.Second))
	}
	var existing entity.RiskWarning
	found, err := riskWarningExists(query, &existing)
	if err != nil {
		return false, nil, err
	}
	if found {
		return false, &existing, nil
	}
	warning := entity.RiskWarning{
		RuleID:     candidate.Rule.RuleID,
		SubjectKey: candidate.Subject,
		Severity:   candidate.Severity,
		UserID:     candidate.UserID,
		Detail:     candidate.Detail,
		CreatedAt:  now,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&warning).Error; err != nil {
		return false, nil, err
	}
	return true, &warning, nil
}

// riskWarningExists 是「该主体是否已有告警」的判定。
//
// 抽成函数是为了让「0 秒节流 = 永远只一次」与「窗口节流」共用同一条查询路径，
// 避免两套写法漂移（一处写 `> ?` 一处写 `>= ?`）。
func riskWarningExists(query *gorm.DB, out *entity.RiskWarning) (bool, error) {
	var row entity.RiskWarning
	err := query.Order("id ASC").First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if out != nil {
		*out = row
	}
	return true, nil
}

// collectUserDebtCandidates 收集「单用户 |balance| >= 阈值」的命中（118 §2.4 触发 1）。
//
// 只扫 balance < 0 的用户，并按欠款额降序取前 riskIndebtedUserScanLimit 个：
// 欠款最深的用户才是告警对象，扫描上限只是防止极端情况下一次查太多行。
func collectUserDebtCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	var users []entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("balance <= ?", -threshold).
		Order("balance ASC").
		Limit(riskIndebtedUserScanLimit).
		Find(&users).Error; err != nil {
		return nil, err
	}
	candidates := make([]riskWarningCandidate, 0, len(users))
	for _, user := range users {
		userID := user.ID
		detail, err := json.Marshal(map[string]any{
			"ruleId":       rule.RuleID,
			"subjectKey":   "user:" + strconv.FormatInt(int64(user.ID), 10),
			"threshold":    threshold,
			"value":        roundRiskAmount(-user.Balance),
			"userId":       user.ID,
			"username":     user.Username,
			"balance":      roundRiskAmount(user.Balance),
			"evaluatedAt":  now.Format(time.RFC3339),
			"evidenceNote": "触发时刻的余额快照，不是实时查询结果（96 §2.3）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  "user:" + strconv.FormatInt(int64(user.ID), 10),
			UserID:   &userID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// collectBatchDebtCandidates 收集「单批次回滚产生的欠款 >= 阈值」的命中（118 §2.4 触发 2）。
//
// 数据源是 billing_records 里批量回滚的**本金**那一笔（118 §5.2）：
// request_id 形如 `admin_grant_reversal_principal_<uid>_<batchId>`。
//
// 「这一笔产生了多少欠款」用账前的欠款与账后的欠款之差算，而不是直接看 amount：
//
//	debtFormed = max(0, -balance_after) - max(0, -balance_before)
//
// 直接看 amount 会把「用户本来就有钱、扣完还有钱」的回滚也算成损失；
// 也算错了「用户本来已经欠款、这次又扣一笔」的情形（那笔要全额计入）。
func collectBatchDebtCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil, nil
	}
	var records []entity.BillingRecord
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("type = ? AND request_id LIKE ? AND created_at >= ?",
			riskBatchReversalBillingType, riskBatchReversalPrincipalPrefix+"%", now.Add(-riskBatchReversalInspectionWindow)).
		Order("id ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	type batchAggregate struct {
		debt  float64
		users map[int32]struct{}
	}
	batches := map[string]*batchAggregate{}
	for _, record := range records {
		batchID := riskBatchIDFromRequestID(record.RequestID)
		if batchID == "" {
			continue
		}
		formed := riskDebtFormed(record.BalanceBefore, record.BalanceAfter)
		if formed <= 0 {
			continue
		}
		agg := batches[batchID]
		if agg == nil {
			agg = &batchAggregate{users: map[int32]struct{}{}}
			batches[batchID] = agg
		}
		agg.debt += formed
		agg.users[record.UserID] = struct{}{}
	}
	// 稳定输出顺序：map 迭代顺序随机，不排序会让「先告警哪个批次」不可复现。
	batchIDs := make([]string, 0, len(batches))
	for id := range batches {
		batchIDs = append(batchIDs, id)
	}
	sort.Strings(batchIDs)
	candidates := make([]riskWarningCandidate, 0, len(batchIDs))
	for _, batchID := range batchIDs {
		agg := batches[batchID]
		if agg.debt < threshold {
			continue
		}
		detail, err := json.Marshal(map[string]any{
			"ruleId":       rule.RuleID,
			"subjectKey":   "batch:" + batchID,
			"batchId":      batchID,
			"threshold":    threshold,
			"value":        roundRiskAmount(agg.debt),
			"debtAmount":   roundRiskAmount(agg.debt),
			"userCount":    len(agg.users),
			"evaluatedAt":  now.Format(time.RFC3339),
			"evidenceNote": "按 balance_before/balance_after 之差累计的本金回滚欠款（118 §5.2）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  "batch:" + batchID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// collectSystemDebtCandidates 收集「系统总欠款 24h 增量 >= 阈值」的命中（118 §2.4 触发 3）。
//
// 基线存在 system_settings（见 riskSystemDebtBaselineAtKey 的注释）：
//   - 没有基线 / 基线已满 24h → **轮换基线并返回空**（首次运行不告警，见变量注释）；
//   - 有基线 → 增量 = 当前总欠款 − 基线，达到阈值才命中。
func collectSystemDebtCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	total, userCount, err := riskCurrentTotalDebt(db)
	if err != nil {
		return nil, err
	}
	baselineRaw := getSystemSettingStringWithDefault(db, riskSystemDebtBaselineValueKey, "")
	baselineAtRaw := getSystemSettingStringWithDefault(db, riskSystemDebtBaselineAtKey, "")
	baseline, baselineErr := strconv.ParseFloat(strings.TrimSpace(baselineRaw), 64)
	baselineAt, atErr := time.Parse(time.RFC3339, strings.TrimSpace(baselineAtRaw))
	if baselineErr != nil || atErr != nil || now.Sub(baselineAt) >= riskSystemDebtWindow || now.Before(baselineAt) {
		// 首次运行 / 基线过期 / 基线时间在未来（时钟回拨）→ 重新记基线，不告警。
		if err := saveRiskSystemDebtBaseline(db, total, now); err != nil {
			return nil, err
		}
		return nil, nil
	}
	increment := total - baseline
	if increment < threshold {
		return nil, nil
	}
	detail, err := json.Marshal(map[string]any{
		"ruleId":       rule.RuleID,
		"subjectKey":   "system:debt",
		"threshold":    threshold,
		"value":        roundRiskAmount(increment),
		"increment":    roundRiskAmount(increment),
		"totalDebt":    roundRiskAmount(total),
		"baselineDebt": roundRiskAmount(baseline),
		"baselineAt":   baselineAt.Format(time.RFC3339),
		"userCount":    userCount,
		"evaluatedAt":  now.Format(time.RFC3339),
		"evidenceNote": "增量按 24h 前的基线快照计算（基线每次轮换间隔 >= 24h）",
	})
	if err != nil {
		return nil, err
	}
	return []riskWarningCandidate{{
		Rule:     rule,
		Subject:  "system:debt",
		Severity: rule.Severity,
		Detail:   string(detail),
	}}, nil
}

// riskCurrentTotalDebt 返回当前系统总欠款额与欠款用户数。
func riskCurrentTotalDebt(db *gorm.DB) (float64, int64, error) {
	var total float64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("balance < 0").
		Select("COALESCE(SUM(-balance), 0)").
		Scan(&total).Error; err != nil {
		return 0, 0, err
	}
	var count int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("balance < 0").Count(&count).Error; err != nil {
		return 0, 0, err
	}
	return total, count, nil
}

// saveRiskSystemDebtBaseline 写入基线（金额 + 时间）。
func saveRiskSystemDebtBaseline(db *gorm.DB, total float64, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := saveSystemSettingValue(tx, riskSystemDebtBaselineValueKey,
			strconv.FormatFloat(roundRiskAmount(total), 'f', 6, 64), "风控：系统总欠款 24h 增量基线（自动维护）"); err != nil {
			return err
		}
		return saveSystemSettingValue(tx, riskSystemDebtBaselineAtKey,
			now.Format(time.RFC3339), "风控：系统总欠款基线轮换时间（自动维护）")
	})
}

// riskDefaultThreshold 返回代码默认阈值（表与设置都缺失时的最后兜底）。
func riskDefaultThreshold(ruleID string) float64 {
	for _, item := range entity.DefaultRiskDebtRules {
		if item.RuleID == ruleID {
			return item.Threshold
		}
	}
	return 0
}

// riskBatchIDFromRequestID 从 `admin_grant_reversal_principal_<uid>_<batchId>` 里取 batchId。
//
// 解析失败返回空串（调用方跳过该行）——**不做猜测**。118 §5.2 明确 request_id 的
// 三段式是幂等键契约；猜错会把两个批次合成一个，产生假告警。
func riskBatchIDFromRequestID(requestID string) string {
	trimmed := strings.TrimSpace(requestID)
	if !strings.HasPrefix(trimmed, riskBatchReversalPrincipalPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(trimmed, riskBatchReversalPrincipalPrefix)
	separator := strings.Index(rest, "_")
	if separator <= 0 || separator == len(rest)-1 {
		return ""
	}
	// uid 段必须是纯数字，否则就是另一种形态的 request_id，不该按批次聚合。
	if _, err := strconv.ParseInt(rest[:separator], 10, 64); err != nil {
		return ""
	}
	return rest[separator+1:]
}

// riskDebtFormed 计算一笔本金扣减形成了多少**新欠款**。
//
//	debtFormed = max(0, -balanceAfter) - max(0, -balanceBefore)
//
// 三种情形：本来有钱、扣完还欠 → 差值就是新欠款；本来就欠、又扣一笔 → 全额计入；
// 扣完仍有钱 → 0。
func riskDebtFormed(balanceBefore, balanceAfter float64) float64 {
	before := 0.0
	if balanceBefore < 0 {
		before = -balanceBefore
	}
	after := 0.0
	if balanceAfter < 0 {
		after = -balanceAfter
	}
	formed := after - before
	if formed < 0 {
		return 0
	}
	return formed
}

// roundRiskAmount 把金额收敛到 6 位小数（与 decimal(18,6) 一致，避免 0.1+0.2 噪声进证据）。
func roundRiskAmount(value float64) float64 {
	return float64(int64(value*1e6+riskAmountRoundingSign(value)*0.5)) / 1e6
}

// riskAmountRoundingSign 给四舍五入提供符号（Go 的 int64 转换对负数是向零取整）。
func riskAmountRoundingSign(value float64) float64 {
	if value < 0 {
		return -1
	}
	return 1
}

// RiskWarningList 返回风控告警列表（管理页只读，96 §2.3 / 121 §4.6）。
//
// 只读：没有任何写路径从这里走。分页参数非法时回落到默认（与管理端其它列表一致），
// 不因为一个 pageSize=abc 就整页 400。
func RiskWarningList(db *gorm.DB, filter RiskWarningFilter) (RiskWarningListResult, error) {
	if db == nil {
		return RiskWarningListResult{}, fmt.Errorf("数据库未初始化")
	}
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > riskWarningListPageSizeMax {
		pageSize = riskWarningListPageSizeDefault
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{})
	if ruleID := strings.TrimSpace(filter.RuleID); ruleID != "" {
		query = query.Where("rule_id = ?", ruleID)
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Notified != nil {
		query = query.Where("notified = ?", *filter.Notified)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("created_at <= ?", *filter.CreatedTo)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return RiskWarningListResult{}, err
	}
	rows := make([]entity.RiskWarning, 0, pageSize)
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return RiskWarningListResult{}, err
	}
	return RiskWarningListResult{Data: rows, Total: total, Page: page, PageSize: pageSize}, nil
}

// RiskWarningDetail 按 id 读取单条告警（96 §4.3 的跳转目标）。
func RiskWarningDetail(db *gorm.DB, id int64) (*entity.RiskWarning, error) {
	if db == nil || id <= 0 {
		return nil, fmt.Errorf("参数 id 无效")
	}
	var row entity.RiskWarning
	if err := db.Session(&gorm.Session{NewDB: true}).First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}
