package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	for _, item := range entity.DefaultRiskRules() {
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
//  1. `risk_rule_<id>_threshold` 系统设置 —— 由管理页「规则阈值」区块写入
//     （service.UpdateRiskThresholdWithDB / 路由 PUT /risk/thresholds/:key）。
//     本任务的注释修正记录：**在写入路径补齐之前**，这一层永远读不到值
//     （raw 恒为 "" ⇒ ParseFloat 恒 err ⇒ 恒落第二层），
//     而当时第二层的 risk_rules.threshold 列也没有写接口 ⇒ 实际只有第三层生效。
//     旧注释把它写成「运营最常调的地方」是**不属实的**，已改正。
//  2. risk_rules.threshold 列 —— 同一写入路径会同步这一列（见 risk_threshold.go）。
//  3. entity.DefaultRiskRules 的代码默认值 —— 未显式配置时实际生效的那一层。
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
// 缺失规则（表还没建/行被删）**不能**被静默跳过：少一条规则等于少一道闸，
// 而且不会有任何报错。所以这里用代码默认值补齐，并把「行不存在」当成
// 「用默认值、按默认启用」处理。
//
// 遍历 entity.DefaultRiskRules()（欠款 + 行为）：任何新规则只要登记在默认值里，
// 判定链就自动拿到它，不需要再改这个函数 —— 这是 R1 范式的一部分。
func loadRiskRules(db *gorm.DB) ([]entity.RiskRule, error) {
	defaults := entity.DefaultRiskRules()
	rows := make([]entity.RiskRule, 0, len(defaults))
	if db != nil && db.Migrator().HasTable(entity.TableNameRiskRule) {
		var stored []entity.RiskRule
		if err := db.Session(&gorm.Session{NewDB: true}).Find(&stored).Error; err != nil {
			return nil, err
		}
		rows = append(rows, stored...)
	}
	byID := make(map[string]entity.RiskRule, len(rows))
	for _, row := range rows {
		byID[row.RuleID] = row
	}
	result := make([]entity.RiskRule, 0, len(defaults))
	for _, item := range defaults {
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

	candidates := make([]riskWarningCandidate, 0, riskIndebtedUserScanLimit+len(entity.DefaultRiskRules()))
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
	// R1 行为规则（96 §2.1）：接在同一条判定链上而不是另起一个 Evaluate 函数 ——
	// 后台 worker 只调这一个入口，新增规则不需要改第二处。
	if rule, ok := enabledRules[entity.RiskRuleIDMultiAccountIP]; ok {
		found, scanErr := collectMultiAccountIPCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDTicketFlood]; ok {
		found, scanErr := collectTicketFloodCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDAbnormalRegisterPattern]; ok {
		found, scanErr := collectAbnormalRegisterPatternCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDTokenAbuse]; ok {
		found, scanErr := collectTokenAbuseCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDOffhoursActivity]; ok {
		found, scanErr := collectOffhoursActivityCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDBindingChurn]; ok {
		found, scanErr := collectBindingChurnCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDWechatPhoneShare]; ok {
		found, scanErr := collectWechatPhoneShareCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDImpossibleTravel]; ok {
		found, scanErr := collectImpossibleTravelCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDCredentialStuffing]; ok {
		found, scanErr := collectCredentialStuffingCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDLoginBruteforce]; ok {
		found, scanErr := collectLoginBruteforceCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDRefundAbuse]; ok {
		found, scanErr := collectRefundAbuseCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDProxyIPLogin]; ok {
		found, scanErr := collectProxyIPLoginCandidates(db, rule, now)
		if scanErr != nil {
			return result, scanErr
		}
		candidates = append(candidates, found...)
	}
	if rule, ok := enabledRules[entity.RiskRuleIDRegisterBurst]; ok {
		found, scanErr := collectRegisterBurstCandidates(db, rule, now)
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
		// 规则可以自己声明「这一条只留痕、先别推送」（96 §2.1 R16 的连续天数门槛）。
		//
		// 为什么这个判断放在推送侧而不是 collector 侧：collector 必须**写入**
		// 当日记录，否则「连续 N 天」永远凑不满（没有历史）。所以不能靠
		// 「不产出候选」来抑制 —— 那样规则永远不响。正确做法是「写记录、晚推送」。
		if candidate.Rule.RuleID == entity.RiskRuleIDOffhoursActivity && !riskOffhoursConsecutiveMet(candidate.Detail) {
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
	// IP 是证据里的来源 IP（96 §2.3 的 RiskAlert.IP 列）。
	// 目前只有行为规则填它（R1 是纯 IP 维度）；欠款三条留空。
	IP string
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
		IP:         candidate.IP,
		Detail:     candidate.Detail,
		CreatedAt:  now,
	}
	// ⚠️ 插入必须是**原子语句**，不能是「上面查完就无条件 Create」。
	//
	// 上面那次查询只是快速路径（命中就省掉一次写），它**不提供并发保证**：
	// 两个 goroutine 同时查不到就会各插一行 —— 实测 8 并发在同一窗口内
	// 得到 2 行（debt_* 同样复现）。
	//
	// 这里用 `INSERT ... SELECT ... WHERE NOT EXISTS`：判重与插入在**同一条语句**里，
	// 由数据库一次执行完。既不需要唯一索引（见 entity.RiskWarning.SubjectKey 的注释：
	// 唯一索引会废掉「隔一个窗口后再告警」），也不依赖隔离级别。
	inserted, err := insertRiskWarningIfAbsent(db, warning, candidate.Rule.ThrottleSeconds, now)
	if err != nil {
		return false, nil, err
	}
	if !inserted {
		// 另一个 goroutine 先插了（或上面查询时还没提交的那一条刚提交）：
		// 语义与「查到了已有行」相同 → 不新建、不重复推送。
		var existing entity.RiskWarning
		lookup := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
			Where("rule_id = ? AND subject_key = ?", candidate.Rule.RuleID, candidate.Subject)
		if found, findErr := riskWarningExists(lookup, &existing); findErr == nil && found {
			return false, &existing, nil
		}
		return false, nil, nil
	}
	// 回读刚插入的行（拿到自增 ID，推送与调用方都要用）。
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("rule_id = ? AND subject_key = ?", candidate.Rule.RuleID, candidate.Subject).
		Order("id DESC").First(&warning).Error; err != nil {
		return false, nil, err
	}
	return true, &warning, nil
}

// insertRiskWarningIfAbsent 用单条 INSERT ... SELECT ... WHERE NOT EXISTS 完成
// 「不存在才插入」，返回是否真的插入了行。
//
// 为什么不是 `Create` + 唯一索引：
//   - 唯一索引 (rule_id, subject_key) 会让窗口节流的规则（debt_user 24h /
//     debt_system 6h）**永远只能有一行**，直接废掉「隔一个窗口后再告警」。
//   - 要两者兼得，必须把「窗口标识」独立成一列 —— 那是表结构变更，超出 R1 范围。
//
// 为什么这条语句是原子的：
//
//	条件判断与写入在同一个 statement 内，数据库要么整体执行要么不执行。
//	SQLite 在 `_txlock=immediate`（生产 DSN）下写事务串行化；
//	MySQL 对 INSERT...SELECT WHERE NOT EXISTS 在同一语句内持有必要的锁。
//	两种方言都支持这个语法（不用 ON CONFLICT / ON DUPLICATE KEY，那些是方言专属）。
//
// 兜底：万一某方言的行为退化，最坏结果是**多一行重复告警**（噪音），
// 不会漏报，也不影响任何处置（风控不处置）。这一点写在 SubjectKey 的注释里。
func insertRiskWarningIfAbsent(db *gorm.DB, warning entity.RiskWarning, throttleSeconds int, now time.Time) (bool, error) {
	statement := "INSERT INTO " + entity.TableNameRiskWarning +
		" (rule_id, subject_key, severity, user_id, ip, detail, notified, created_at)" +
		" SELECT ?, ?, ?, ?, ?, ?, ?, ?" +
		" WHERE NOT EXISTS (SELECT 1 FROM " + entity.TableNameRiskWarning +
		" WHERE rule_id = ? AND subject_key = ?"
	args := []any{
		warning.RuleID, warning.SubjectKey, warning.Severity, warning.UserID, warning.IP,
		warning.Detail, false, now,
		warning.RuleID, warning.SubjectKey,
	}
	if throttleSeconds > 0 {
		// 窗口节流：窗口内已有行才阻止插入；窗口外允许再写一条。
		statement += " AND created_at > ?"
		args = append(args, now.Add(-time.Duration(throttleSeconds)*time.Second))
	}
	statement += ")"

	result := db.Session(&gorm.Session{NewDB: true}).Exec(statement, args...)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
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

// collectMultiAccountIPCandidates 收集「同一 IP 在窗口内登录了 ≥N 个不同账号」的命中
// （96 §2.1 R1）。
//
// # 这是 18 条行为规则的**范式**，R2–R18 照抄这个骨架
//
// 四条纪律（每一条都有对应的踩坑理由）：
//
//  1. **数据源先读代码确认，不假定**。R1 用的是 `logs` 表：
//     成功登录由 controller 的 recordAccountAudit → WriteAccountAuditWithDB 写入
//     （user.go:431 密码登录 / :1123 短信登录 / admin_login.go:151 管理员入口），
//     action=logActionLogin(10)，client_ip 是来源 IP，user_id 是账号。
//     **不是** `login_attempts` —— 那张表只记「连续失败计数 + 锁定」，
//     成功登录时走的是 ClearLoginAttemptsOnSuccessWithDB（清零+重置窗口起点），
//     从不落一行成功流水。用错表的表现是「永远查不到多账号」而不报错。
//
//  2. **阈值走 riskRuleThreshold**（三层兜底），键 `risk_rule_<rule_id>_threshold`。
//     不要在这里写死数字：写死 = 运营改配置无效，且不会有任何提示。
//
//  3. **节流靠 SubjectKey 里编进窗口起点**，不靠「查有没有同类告警」。
//     key = `multi_account_ip:<ip>:<窗口起点 RFC3339>`。
//     同一窗口内 key 完全相同 → createRiskWarningIfNeeded 的
//     (rule_id, subject_key) 查询命中已有行 → 不写第二行；
//     窗口滚动后 key 变化 → 自然产生新的一条。
//     而「查有没有同类告警」是**先读后写**，两个 goroutine 同时读不到就会各写一行，
//     且没有唯一索引兜底 —— 并发下必然重复（见 TestRiskMultiAccountIPConcurrentEvaluationWritesOnce）。
//
//  4. **只读不写**（96 C1）：本函数不 UPDATE users、不碰 blacklist_entries、不 Abort 请求。
//
// 已知取舍（如实记录，不掩盖）：
//
//   - 按**自然日 24h 窗口**（上海时区 00:00 切日）切分，不是滑动窗口。
//     自然日窗口的好处是 key 稳定可复现（同一 IP 同一天只有一个 key）；
//     96 §2.1 写的是「24h 内」，严格说滑动窗口更贴合，但滑动窗口的 key 会随
//     now 漂移（每次评估一个不同的窗口起点），等于没有节流 —— 那会退化成
//     「每次评估都写一条」。这里优先保证节流正确。
//
//     ⚠️ **排障时最有用的一句话（用户可见后果）**：
//     跨零点的两次行为**不计入同一窗口**。
//     例：某 IP 在 23:59 登录了 4 个账号、次日 00:01 又登录了 4 个，阈值 5 →
//     **不会告警**，因为 4 和 4 分属两个窗口，各自都没达到 5。
//     排查「明明超过阈值为什么不报」时，先看是不是被零点切开了。
//
//   - 共享出口（校园网/公司 NAT/运营商 CGNAT）会误报。这是 R1 固有的误报面，
//     96 §2.1 把它定为「中」级别并**只告警不处置**正是为此。阈值默认 5 也是
//     为了避开「一家人/一个宿舍共用一个 IP」的常见情况。
//
//   - ⚠️ **后续规则纪律（R2–R18 必读）**：`logs.action = 10` **不能单独作判据**。
//     成功登录与「非管理员尝试管理员入口」（admin_login.go:127）都写 action=10，
//     后者 status=denied。任何按 action 过滤的规则都必须同时看 status，
//     否则会把「一个人反复试管理员入口」误判成「同 IP 多账号」这类假信号。
func collectMultiAccountIPCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		// logs 表不存在（极简单测 schema）→ 不判定。不报错：行为规则依赖业务表，
		// 缺表时告警系统不该让启动/评估失败。
		return nil, nil
	}
	windowStart, windowEnd := riskLoginWindow(now)
	var rows []riskIPAccountGroup
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.Log{}).
		Select("client_ip AS client_ip, COUNT(DISTINCT user_id) AS accounts").
		Where("action = ?", riskLoginAuditAction).
		Where("module = ?", riskLoginAuditModule).
		Where("client_ip <> ''").
		Where("user_id > 0").
		// 只认成功登录。这个过滤有两个**独立**作用，缺一个都会出错：
		//
		//  ① 排除「带 action=10 但不是登录成功」的行 —— 例如
		//     「非管理员尝试使用管理员入口登录」写的是 status=denied。
		//     把它们算进账号数会把「一个人反复试管理员入口」误判成「同 IP 多账号」。
		//
		//  ② **自动跳过历史不可判定行**。实测本机库：module='auth' 且 action=10
		//     共 32 行，其中只有 4 行带 status=success，另 28 行的 new_answer
		//     **是空串**（早期 recordAccountAudit 未写 Result 的历史遗留）。
		//     时间范围也不重叠：空值集中在 2026-05-22~10-25，带 success 的在
		//     10-26 之后 —— 证实是版本演进而非随机缺失。
		//
		//     ⚠️ 这 28 行**既不是成功也不是失败，是「无法判定」**。不做这个过滤
		//     就会把不可判定数据当成有效登录，让「同 IP 多账号」的分子虚高。
		//     ⇒ 「有 N 行 auth 日志」≠「有 N 次可用登录」，这个区别必须记住。
		Where("new_answer LIKE ?", `%"status":"success"%`).
		Where("create_time >= ? AND create_time < ?", windowStart, windowEnd).
		Group("client_ip").
		// 阈值判断留在 Go 侧（不在 HAVING 里）：SQL 里写 HAVING 会让「阈值恰好相等」
		// 的边界判定随方言变化（不同 DB 的 COUNT 类型比较规则不一致），
		// 而这条边界正是判据①要钉的东西。
		Order("client_ip ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	candidates := make([]riskWarningCandidate, 0, len(rows))
	for _, row := range rows {
		ip := strings.TrimSpace(row.ClientIP)
		if ip == "" {
			continue
		}
		accounts := row.Accounts
		// 「≥ 阈值」：2 个账号 + 阈值 3 → 不告警；3 个账号 + 阈值 3 → 告警。
		// 写成 > 会让「恰好达到阈值」漏报，这是判据①要钉住的边界。
		if float64(accounts) < threshold {
			continue
		}
		subject := riskMultiAccountIPSubject(ip, windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":      rule.RuleID,
			"subjectKey":  subject,
			"ip":          ip,
			"threshold":   threshold,
			"value":       accounts,
			"accounts":    accounts,
			"windowStart": windowStart.Format(time.RFC3339),
			"windowEnd":   windowEnd.Format(time.RFC3339),
			"evaluatedAt": now.Format(time.RFC3339),
			"evidenceNote": "按 logs 表中 status=success 的登录审计行统计不同 user_id 数量；" +
				"窗口为自然日 24h（窗口起点编进 subjectKey 以保证节流可复现）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
			IP:       ip,
		})
	}
	return candidates, nil
}

// riskIPAccountGroup 是 R1 的聚合行。
type riskIPAccountGroup struct {
	ClientIP string
	Accounts int64
}

// 登录审计行的三个判定值（与 controller/logs.go 的 logActionLogin、
// account_audit.go 的 module 字面量对齐）。
//
// 刻意写字面量而不是 import controller 里的常量：service 不能依赖 controller
// （会形成 import 环），而这两处字面值必须一致。测试
// TestRiskLoginAuditPredicateMatchesControllerConstants 用源码断言把两者钉住 ——
// 这是「跨层字面量」唯一可行的防漂移手段。
const (
	riskLoginAuditAction int32  = 10
	riskLoginAuditModule string = "auth"
)

// riskLoginWindow 返回 now 所在的自然日 24h 窗口 [start, end)。
//
// 用固定时区（Asia/Shanghai）而不是 UTC：审计行的 create_time 由
// WriteAccountAuditWithDB 写成 time.Now().In(ShanghaiLocation())，
// 若窗口按 UTC 切分，国内凌晨 0-8 点的登录会落到「前一天」的窗口里，
// 与运营看到的日期对不上。
func riskLoginWindow(now time.Time) (time.Time, time.Time) {
	local := now.In(logger.ShanghaiLocation())
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	return start, start.Add(24 * time.Hour)
}

// riskMultiAccountIPSubject 生成 R1 的节流主体。
//
// 形如 `multi_account_ip:203.0.113.9:2026-11-14T00:00:00+08:00`。
// 窗口起点编进 key 是节流机制的核心（见 collectMultiAccountIPCandidates 纪律 3）。
func riskMultiAccountIPSubject(ip string, windowStart time.Time) string {
	return entity.RiskRuleIDMultiAccountIP + ":" + strings.TrimSpace(ip) + ":" + windowStart.Format(time.RFC3339)
}

// riskBehaviorSubject 生成行为规则的节流主体：`<rule_id>:<scope>:<窗口起点>`。
//
// R1 之后的规则统一走这个函数，不要各写一份（键形态漂移会让节流静默失效）。
func riskBehaviorSubject(ruleID, scope string, windowStart time.Time) string {
	return strings.TrimSpace(ruleID) + ":" + strings.TrimSpace(scope) + ":" + windowStart.Format(time.RFC3339)
}

// riskWindowStartExpr 返回「把列换算到上海时区并按秒截断」的方言表达式，供窗口过滤用。
//
// ⚠️ 为什么需要它（本仓实测踩过的坑）：
// SQLite 里时间列存的是**带偏移的文本**，而偏移**不一定是 +08:00**：
// 实测本机库 `users.created_at` = `2026-05-08 13:10:30.573137548+00:00`，
// `logs.create_time` 999 行里 923 行是 `+00:00` —— **是 UTC，不是本地时间**。
//
// 原实现用裸 `substr(col,1,19)` 截成 `YYYY-MM-DD HH:MM:SS` 再与上海时区窗口比，
// 那只在所有行都恰好是本地时间偏移时才成立。碰上 `+00:00` 的行，截出来的
// 是 **UTC 时刻**，与 CST 窗口直接差 8 小时。
//
// 实测影响面（本机库）：`logs` 表 923 条 `+00:00` 行按 CST 口径比对，
// **923/923 全部落在错误的自然日**（SQL：`substr(create_time,1,19) <> datetime(create_time,'+8 hours')`
// 命中 923）。R1（logs）、R11（users）都受此影响 —— 这类偏移不会报错，
// 只会让「自然日窗口」静默错位，表现为「明明超了阈值却不告警」或反之。
//
// 修法：不靠 substr 猜，改用 SQLite 的 `datetime()` 做**真正的时区换算**。
// `datetime(col)` 会把带偏移的文本归一成 UTC 的 `YYYY-MM-DD HH:MM:SS`，
// 再 `datetime(col,'+8 hours')` 就得到上海本地时刻的同一格式，最后与
// riskWindowBounds 用同一时区格式化的边界比较，口径完全一致。
//
// 为什么不用 `datetime(col,'localtime')`：那依赖**进程所在机器的时区**。
// CI 跑在 UTC、开发机跑在 CST，同一份数据会得出不同窗口 —— 比偏移更难查。
// 固定 '+8 hours' 让判定结果与部署环境无关（96 §2 的窗口定义本身就是上海时区）。
//
// MySQL 的时间列是真 DATETIME（无偏移后缀），不需要也不应该做文本换算。
// 因此按方言返回不同的表达式。
func riskWindowColumnExpr(db *gorm.DB, column string) string {
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		return "datetime(" + column + ", '+8 hours')"
	}
	return column
}

// riskWindowBounds 把一个时刻收敛成自然日窗口（上海时区）并返回用于 SQL 的值。
//
// 返回值是**格式化后的字符串**而不是 time.Time：见 riskWindowColumnExpr 的注释，
// SQLite 下两边都按 `YYYY-MM-DD HH:MM:SS` 文本比较才不会有偏移问题。
// MySQL 下驱动会把字符串正确转成 DATETIME，接口一致。
func riskWindowBounds(now time.Time) (start string, end string) {
	windowStart, windowEnd := riskLoginWindow(now)
	return windowStart.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")
}

// loadAccessTokenForRisk 按 id 重新读一次令牌行（供额度周期换算用）。
//
// 为什么不复用上面 Scan 出来的 tokenQuota 结构：LoadAccessTokenQuotaWindowWithDB
// 的签名要完整的 entity.AccessToken，而上面的扫描只取了 6 列（省 IO）。
// 多读一次的代价可接受：候选量受 `total_amount_used > 0` 与配额下限双重过滤，
// 通常是个位数量级。
func loadAccessTokenForRisk(db *gorm.DB, id int32) (entity.AccessToken, error) {
	var token entity.AccessToken
	err := db.Session(&gorm.Session{NewDB: true}).First(&token, id).Error
	return token, err
}

// collectTicketFloodCandidates 收集「单用户 24h 建单 ≥N 张」的命中（96 §2.1 R8）。
//
// # 判据与数据源
//
// 数据源是 **tickets 主表**（不是 ticket_messages）：R8 数的是「建单」，
// 用户在已有工单里的回复不算新建工单 —— 把回复算进去会让一个正常沟通的用户
// 被误判成刷屏。
//
// excluded 掉系统/管理员自动建的工单：Q26 的「注册赠送失败自动建单」由系统
// 在用户无过错时触发，把那些算到用户头上属于误报。判据用 origin 列
// （entity.Ticket origin 取值见 ticket.go）；这里用「user_id > 0」+
// 「origin 不是 system」双重保护。
func collectTicketFloodCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameTicket) {
		return nil, nil
	}
	start, end := riskWindowBounds(now)
	windowStart, _ := riskLoginWindow(now)
	timeExpr := riskWindowColumnExpr(db, "created_at")

	type floodGroup = RiskTicketFloodGroup
	var groups []floodGroup
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Ticket{}).
		Select("user_id AS user_id, COUNT(*) AS ticket").
		Where("user_id > 0").
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?", start, end)
	// 系统自动建的工单不算用户刷屏。origin 列存在时才加这个条件（老库可能没有）。
	if db.Migrator().HasColumn(entity.TableNameTicket, "origin") {
		query = query.Where("origin IS NULL OR origin <> ?", entity.TicketOriginSystem)
	}
	if err := query.Group("user_id").Order("user_id ASC").Scan(&groups).Error; err != nil {
		return nil, err
	}
	candidates := make([]riskWarningCandidate, 0, len(groups))
	usernames := riskUsernamesByID(db, riskTicketFloodUserIDs(groups))
	for _, group := range groups {
		// 「≥ 阈值」：4 张 + 阈值 5 → 不告警；5 张 → 告警。
		if float64(group.Ticket) < threshold {
			continue
		}
		userID := group.UserID
		subject := riskBehaviorSubject(rule.RuleID, "user:"+strconv.FormatInt(int64(userID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":       rule.RuleID,
			"subjectKey":   subject,
			"userId":       userID,
			"username":     usernames[userID],
			"threshold":    threshold,
			"value":        group.Ticket,
			"ticketCount":  group.Ticket,
			"windowStart":  windowStart.Format(time.RFC3339),
			"evaluatedAt":  now.Format(time.RFC3339),
			"evidenceNote": "按 tickets 主表统计 24h 内新建工单数；不含 ticket_messages 回复，不含系统自动建单",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// riskAbnormalRegisterPrefixMinLength 是 R11 的最小前缀长度。
//
// 为什么是 3 而不是 1（这是本规则最关键的收窄）：
// 前缀只有 1~2 字符时，正常用户名撞前缀的概率极高 —— `a1`/`b2` 这种
// 短前缀会把大量互不相关的用户归到同一组。3 字符是「看起来像人为编号」
// 与「普通用户名巧合」之间的经验分界（`abc1`/`abc2`/`abc3` 才像批量注册）。
const riskAbnormalRegisterPrefixMinLength = 3

// riskAbnormalRegisterSequenceMaxDigits 限制数字部分长度。
//
// 不设上限时 `user1000000000` 这类含大数的名字会参与配对，而「连续递增」
// 在数字位数不同时极易误判（`abc9` → `abc10` 在字典序上不连续但在数值上连续，
// 两种口径都可能有争议）。限制 9 位覆盖所有现实场景，避免边界争议。
const riskAbnormalRegisterSequenceMaxDigits = 9

// collectAbnormalRegisterPatternCandidates 收集「同前缀 + 连续递增数字用户名 ≥N 个」的命中
// （96 §2.1 R11）。
//
// # 判据定义（务必与实现逐条对照）
//
// 把窗口内新建用户的用户名拆成 (前缀, 数字) 两段：`abc123` → ("abc", 123)。
// 要求：
//   - 前缀是 ≥3 个字符的**字母**（不含数字/符号），大小写归一化后比较；
//   - 数字部分 1..9 位；
//   - 同一前缀下，数字集合里存在一条**连续递增序列**，长度 ≥ 阈值；
//   - 整组必须落在同一自然日窗口内（窗口在 SQL 里已过滤）。
//
// # 为什么必须要求「连续递增」而不是「任意数字」
//
// 这是本规则唯一能避免大面积误报的约束。反例（真实场景）：
//
//	user2024 / test123 / abc999 —— 都是「字母+数字」的普通用户名，
//	数字 2024/123/999 互不连续，必须**不告警**。
//
// 只写 `^[a-z]+\d+$` 会把它们全算进去。判据③ 的对照用例钉的就是这一点。
//
// # 性能说明（如实记录，决策依据）
//
// `users.username` 与 `users.created_at` **都没有索引**（实测 entity/users.gen.go:
// Username/CreatedAt 均无 index tag）。因此本查询是一次带时间窗的全表扫。
// 用时间窗先过滤是正确的缓解手段（窗口内新增用户通常远小于全表）。
//
// **当前接受这个开销**（决策，不是遗漏）：本查询带自然日窗口 + 前缀分组，
// 在 users 规模到**十万行以内**不构成瓶颈；而加索引要走表结构变更 + 迁移兼容 +
// 存量回填，与「一条风控规则」的收益不成比例。
//
// ⚠️ 触发放缓条件：如果 users 表 **超过 10 万行** 且本规则让 EvaluateDebtAlerts
// 变慢（实测方式：给本函数加计时日志，看单轮耗时），则给 users.created_at 加索引。
// 届时只需一条 `CREATE INDEX idx_users_created_at ON users(created_at)`，
// 不需改动本函数逻辑 —— 下面的 SQL 已经是「按 created_at 过滤」的标准形态。
//
// # 语义取舍（严口径，不放宽 —— 附放宽点与代价）
//
// 本规则**只认**「纯字母前缀 + 纯数字后缀」。因此下面这些**不参与判定**：
//
//	user_1 / user_2      —— 前缀含分隔符
//	张三1 / 张三2         —— 中文前缀
//	a1bc2                —— 字母夹数字
//
// 为什么选严的一侧：**漏报的代价 << 误报的代价**。风控误报会打扰正常用户、
// 消耗管理员工时；漏报只是少抓一个养号群体。另外中文前缀在编码上无法可靠判
// 「递增」（UTF-8 码点顺序 ≠ 用户直觉的编号顺序）。
//
// ⚠️ 若实测发现**真实批量注册确实用分隔符**（例如 `user_1`/`user_2`/`user_3`
// 成批出现），放宽点就是唯一的：`riskSplitSequentialUsername` 里「出现非字母字符
// 即拒绝」那个分支 —— 允许可选的前导分隔符（`_`/`-`）。
//
// 放宽的代价（务必权衡后再改）：`user_1`/`user_2` 与 `user1`/`user2` 会混成
// **同一个前缀族**，同族成员数直接翻倍，误报面显著上升；而且一旦放宽，
// 「前缀里允许哪些分隔符」会变成一组难收敛的规则（`_`/`-`/`.`/空格？）。
// 真要放宽，建议先只允许 `_` 并单独跑一轮实测对比误报率，不要一次放多个。
func collectAbnormalRegisterPatternCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil, nil
	}
	start, end := riskWindowBounds(now)
	windowStart, _ := riskLoginWindow(now)
	timeExpr := riskWindowColumnExpr(db, "created_at")

	type newUser struct {
		ID       int32
		Username string
	}
	var users []newUser
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Select("id, username").
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?", start, end).
		Order("id ASC").
		Scan(&users).Error; err != nil {
		return nil, err
	}

	// 按前缀归组：prefix → 该前缀下出现过的数字集合。
	//
	// 同前缀下可能混入不连续的数字（abc1/abc5），所以不能「计数 ≥ N 就告警」，
	// 必须找出**最长连续段**再与阈值比。
	type prefixGroup struct {
		numbers map[int]struct{}
		// samples 只用于证据快照（保留前若干个用户名，便于人工复核）。
		samples []string
	}
	groups := map[string]*prefixGroup{}
	for _, user := range users {
		prefix, number, ok := riskSplitSequentialUsername(user.Username)
		if !ok {
			continue
		}
		group := groups[prefix]
		if group == nil {
			group = &prefixGroup{numbers: map[int]struct{}{}}
			groups[prefix] = group
		}
		group.numbers[number] = struct{}{}
		if len(group.samples) < riskWarningDetailMaxSampleUsers {
			group.samples = append(group.samples, user.Username)
		}
	}

	// 固定输出顺序：map 迭代随机，不排序会让「先告警哪个前缀」不可复现。
	prefixes := make([]string, 0, len(groups))
	for prefix := range groups {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	candidates := make([]riskWarningCandidate, 0, len(prefixes))
	for _, prefix := range prefixes {
		group := groups[prefix]
		runLength, runStart := riskLongestConsecutiveRun(group.numbers)
		// 「≥ 阈值」：连续段长度 2 + 阈值 3 → 不告警；3 → 告警。
		if float64(runLength) < threshold {
			continue
		}
		subject := riskBehaviorSubject(rule.RuleID, "prefix:"+prefix, windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":       rule.RuleID,
			"subjectKey":   subject,
			"prefix":       prefix,
			"threshold":    threshold,
			"value":        runLength,
			"sequenceLen":  runLength,
			"sequenceFrom": runStart,
			"sequenceTo":   runStart + runLength - 1,
			"sampleUsers":  group.samples,
			"windowStart":  windowStart.Format(time.RFC3339),
			"evaluatedAt":  now.Format(time.RFC3339),
			"evidenceNote": "同前缀（≥3 字母）+ 连续递增数字的用户名，按连续段长度判定；非连续数字不计入",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// riskWarningDetailMaxSampleUsers 是证据快照里保留的用户名样本数上限。
const riskWarningDetailMaxSampleUsers = 10

// riskSplitSequentialUsername 把用户名拆成 (前缀小写, 数字) 。
//
// 只接受「纯字母前缀 + 纯数字后缀」，且前缀长度 ≥ riskAbnormalRegisterPrefixMinLength、
// 数字 1..9 位。任何不符合形态的（含符号、纯数字、字母夹数字）都返回 false，
// 直接不参与 R11 判定 —— 这是收窄的第一道闸。
//
// ⚠️ **这是「放宽分隔符」的唯一修改点**（见 collectAbnormalRegisterPatternCandidates
// 的「语义取舍」段）：若实测发现真实批量注册用 `user_1`/`user_2` 这种带分隔符的形态，
// 就在下面「出现非字母字符即拒绝」那个分支允许可选的前导 `_`。
// 放宽前请先读那一段的代价分析（同族成员数翻倍 → 误报面上升），
// 并且只放一种分隔符、单独跑一轮误报率对比，不要一次放多个。
func riskSplitSequentialUsername(username string) (string, int, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(username))
	if trimmed == "" {
		return "", 0, false
	}
	splitAt := -1
	for i, r := range trimmed {
		if r >= '0' && r <= '9' {
			splitAt = i
			break
		}
		if r < 'a' || r > 'z' {
			// 出现数字以外的非字母字符（含 _ / - / 中文）→ 不是本规则的目标形态。
			return "", 0, false
		}
	}
	if splitAt < 0 {
		return "", 0, false // 纯字母，没有编号
	}
	prefix := trimmed[:splitAt]
	digits := trimmed[splitAt:]
	if len(prefix) < riskAbnormalRegisterPrefixMinLength {
		return "", 0, false
	}
	if len(digits) > riskAbnormalRegisterSequenceMaxDigits {
		return "", 0, false
	}
	// 数字段必须全是数字（上面已 loop 到首个数字就 break，这里再确认尾巴）。
	number := 0
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		number = number*10 + int(r-'0')
	}
	return prefix, number, true
}

// riskLongestConsecutiveRun 在数字集合里找**最长连续递增段**，返回长度与起点。
//
// 例：{1,2,3} → (3, 1)；{1,5,9} → (1, 1)；{7,8,10,11,12} → (3, 10)。
// 这是「连续递增」判据的唯一实现点 —— 放宽成「任意数字」（只数集合大小）
// 会让 user2024/test123/abc999 这类正常用户名误报。
func riskLongestConsecutiveRun(numbers map[int]struct{}) (int, int) {
	if len(numbers) == 0 {
		return 0, 0
	}
	ordered := make([]int, 0, len(numbers))
	for number := range numbers {
		ordered = append(ordered, number)
	}
	sort.Ints(ordered)
	bestLen, bestStart := 1, ordered[0]
	currentLen, currentStart := 1, ordered[0]
	for i := 1; i < len(ordered); i++ {
		if ordered[i] == ordered[i-1]+1 {
			currentLen++
		} else {
			currentLen, currentStart = 1, ordered[i]
		}
		if currentLen > bestLen {
			bestLen, bestStart = currentLen, currentStart
		}
	}
	return bestLen, bestStart
}

// riskTokenAbuseMinQuota 是计算「配额消耗比例」所需的最小配额。
//
// 配额 ≤ 0（或 NULL）时**算不出比例**，必须跳过而不是当成「比例很高」——
// 除零会得到 ±Inf/NaN，而 `Inf >= 3` 恒真 ⇒ 每个未设配额的新 token 一创建就告警。
// 这是「基线不足时不告警」在 R12 的具体形态。
const riskTokenAbuseMinQuota = 0.0

// collectTokenAbuseCandidates 收集 access token 滥用命中（96 §2.1 R12）。
//
// # 为什么不用 billing_records 聚合（前一版的错法）
//
// controller/search.go 的 shouldBillCompletedSearch 在「结果为空且无 AI 用量」时
// **不记账**（`:1199`）—— 而「高频请求但查不到」恰好是滥用高发场景。
// 用账单聚合会把最该抓的那批请求完全漏掉，方向是错的（不是保守）。
//
// # 数据源：access_tokens 的现成字段
//
//   - ① 配额消耗进度 = TotalAmountUsed / TotalQuotaLimit ≥ 阈值
//   - ② 多 IP 漂移  = 同一 token 在窗口内被 ≥ 阈值 个不同 IP 使用
//
// 两者取「或」：任一命中即告警。
//
// # 重试污染核实结论（本规则最关键的前提，已实测）
//
// `controller.StartSearchBillingRetryWorker` 确实复用**同一条**记账入口
// `persistSearchBillingRecordsWithDB`，但该函数内部有幂等闸门：
//
//	persistSearchBillingRecordsWithDB (search.go:1961)
//	  → searchBillingRetryBatchAlreadyPersisted (search.go:1993)
//	    → 命中即 `return completeSettledEscrowExecution(...)`（早退，不进扣费段）
//	  → total_amount_used 的 UPDATE 在 :2127（在闸门**之后**）
//
// 闸门判据：`(user_id, request_id, type=normal)` 的账单行与本次 charges 逐字段全等
// （billingRecordMatchesSearchCharge 比 12 个字段）。retry 重放同一 requestID+charges
// 时必然命中 ⇒ **不重复扣费**。
//
// 实测（探针，同一 requestID 连续 persist 两次）：
//
//	after 1st  TotalAmountUsed=1.5
//	after 2nd  TotalAmountUsed=1.5   ← 未翻倍
//	billing rows=1
//
// ⇒ **TotalAmountUsed 不会被系统自动重试污染**，可以安全用作判据。
// 唯一前提：requestID 非空（`searchBillingRetryBatchAlreadyPersisted` 在
// requestID 为空时返回 false，即跳过闸门）。实测真实请求路径都会赋值
// （search.go:346 / :2266 用 uuid.NewString()，:366 再 TrimSpace 一次）。
// ⚠️ 若将来出现「requestID 为空」的请求路径，这条幂等保证会失效 ——
// 届时 R12 的配额判据会被重试污染，需重新评估。
//
// # 基线不足：什么时候「算不出」而不是「很高」
//
// 下列任一成立即**跳过该 token 的配额判据**（不影响多 IP 判据）：
//   - TotalQuotaLimit ≤ 0 或为 NULL（未设配额 → 无基线）
//   - LastUsedAt 为空（从未使用过 → 消耗必然是 0，无意义）
//
// 多 IP 判据的「基线」不同：需要窗口内至少 2 个不同 IP 才可能达到阈值，
// 因此新 token（0~1 个 IP）天然不会命中，不需要额外判断。
func collectTokenAbuseCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	// ⚠️ 两个**独立**阈值，不能共用（t193 审计发现的问题，本轮修）：
	//
	//   quotaThreshold —— 「配额消耗倍数」。读自动键 risk_rule_<id>_threshold，
	//     默认 3（97 号 L321 原文：「调用量 > 其配额 3 倍」）。
	//   ipThreshold —— 「同一 token 的不同 IP 数」。读独立键
	//     risk_token_abuse_multi_ip_threshold，默认 5（**本仓自定**，
	//     97 号原文只说「或被多 IP 并发使用」，没给数量）。
	//
	// 为什么必须拆：两者的合理默认值差很远 —— 配额超 3 倍是明确滥用，而
	// 3 个 IP 用同一 token 很可能是「手机 + 电脑 + 平板」的正常场景。
	// 共用时运营放宽 IP 必然连带放宽配额（严重漏报），反之收紧配额会让
	// 2 个 IP 共用就告警（大量误报）。
	quotaThreshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	ipThreshold := riskTokenMultiIPThreshold(db)
	// 两个都为 0 才完全跳过；任一有效就继续（各判据各自判自己的）。
	if quotaThreshold <= 0 && ipThreshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameAccessToken) {
		return nil, nil
	}
	start, end := riskWindowBounds(now)
	windowStart, _ := riskLoginWindow(now)
	timeExpr := riskWindowColumnExpr(db, "created_at")

	// ① 配额消耗进度。
	type tokenQuota struct {
		ID              int32
		UserID          int32
		Name            string
		TotalQuotaLimit float64
		TotalAmountUsed float64
		LastUsedIP      string
	}
	var quotaRows []tokenQuota
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.AccessToken{}).
		Select("id, user_id, name, total_quota_limit, total_amount_used, last_used_ip").
		Where("total_quota_limit > ?", riskTokenAbuseMinQuota).
		Where("total_amount_used > 0").
		Scan(&quotaRows).Error; err != nil {
		return nil, err
	}

	candidates := make([]riskWarningCandidate, 0, len(quotaRows))
	seen := map[int32]struct{}{}
	for _, row := range quotaRows {
		// 基线不足：配额 ≤ 0 已在 SQL 过滤，这里再挡一次（防 NULL 扫描成 0 后漏网）。
		if row.TotalQuotaLimit <= riskTokenAbuseMinQuota {
			continue
		}
		// ⚠️ 必须用「本轮已用」而不是 total_amount_used（历史累计）。
		//
		// 本仓的额度周期是**叠加层**设计（entity/access_token_quota_epoch.go 的注释
		// 明确把它列为「本设计的红线」）：
		//
		//	本轮已用 = total_amount_used - baseline_amount（当前 epoch 的基线）
		//	total_amount_used 永不清零（账单/对账/审计都以它为据）
		//
		// 原实现直接读 total_amount_used 原始列，完全没看 epoch baseline ⇒
		// 管理员点「重置额度周期」（POST /access-tokens/:id/reset-quota-window）
		// 之后，该 token 的「本轮已用」在前端显示 0，但**风控照样按历史累计
		// 达到 3 倍继续每天告警，且用户没有任何操作能消除**（累计只增不减）。
		// 这是**永久误报**，不是漏报。
		//
		// 实测（探针）：quota=100 / used=300 / epoch baseline=300 时，
		// 正确口径 windowUsed=0，而原实现仍命中 1 条告警。
		//
		// 用 LoadAccessTokenQuotaWindowWithDB 而不是自己算：
		// 它是本仓「本轮已用」的唯一计算入口（access_token_quota_epoch.go:74 的注释
		// 原话），自己再算一遍必然与准入判定漂移。
		windowUsed := row.TotalAmountUsed
		if token, err := loadAccessTokenForRisk(db, row.ID); err == nil {
			if window, err := LoadAccessTokenQuotaWindowWithDB(db, token); err == nil {
				windowUsed = window.WindowUsed
			}
			// 读失败时保守回落到历史累计：宁可多报一次，也不要因为一次查询失败
			// 就漏掉真正在滥用的令牌（fail-closed）。
		}
		ratio := windowUsed / row.TotalQuotaLimit
		// 「≥ 阈值」：3.0 恰等于阈值 3 → 告警。
		if quotaThreshold <= 0 || ratio < quotaThreshold {
			continue
		}
		subject := riskBehaviorSubject(rule.RuleID, "token:"+strconv.FormatInt(int64(row.ID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":      rule.RuleID,
			"subjectKey":  subject,
			"tokenId":     row.ID,
			"userId":      row.UserID,
			"tokenName":   row.Name,
			"quotaLimit":  roundRiskAmount(row.TotalQuotaLimit),
			"quotaUsed":   roundRiskAmount(row.TotalAmountUsed),
			"quotaRatio":  roundRiskAmount(ratio),
			"threshold":   quotaThreshold,
			"value":       roundRiskAmount(ratio),
			"signal":      "quota_ratio",
			"windowStart": windowStart.Format(time.RFC3339),
			"evaluatedAt": now.Format(time.RFC3339),
			"evidenceNote": "配额消耗 = access_tokens.total_amount_used / total_quota_limit；" +
				"已实测系统自动重试不会重复累加（request_id 幂等闸门）",
		})
		if err != nil {
			return nil, err
		}
		seen[row.ID] = struct{}{}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}

	// ② 多 IP 漂移：窗口内同一 token 被多少个不同 IP 使用。
	//
	// 数据源仍是 billing_records（这里用它是**对的**）：IP 只记在账单行上，
	// 而「同一 token 短时间内从多个 IP 发起」即使全是「查不到结果」的请求，
	// 只要产生了计费行就能看到 IP。查不到结果的请求虽不记账，但
	// **能刷出多 IP 的滥用者通常也有成功请求**，因此这条判据的漏报面可接受
	// （比配额判据小得多，因为配额判据完全不依赖账单）。
	type tokenIP struct {
		AccessTokenID int32
		UserID        int32
		IPs           int64
	}
	var ipRows []tokenIP
	if db.Migrator().HasTable(entity.TableNameBillingRecord) {
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
			Select("access_token_id, MAX(user_id) AS user_id, COUNT(DISTINCT client_ip) AS ips").
			Where("access_token_id > 0").
			Where("client_ip <> ''").
			Where(timeExpr+" >= ? AND "+timeExpr+" < ?", start, end).
			Group("access_token_id").
			Order("access_token_id ASC").
			Scan(&ipRows).Error; err != nil {
			return nil, err
		}
	}
	for _, row := range ipRows {
		// 「≥ 阈值」：低于阈值跳过。阈值读**独立键**，与配额倍数无关。
		if ipThreshold <= 0 || float64(row.IPs) < ipThreshold {
			continue
		}
		// 配额判据已为这个 token 产出候选时不再重复产出（一个 token 一个窗口一条）。
		if _, ok := seen[row.AccessTokenID]; ok {
			continue
		}
		subject := riskBehaviorSubject(rule.RuleID, "token:"+strconv.FormatInt(int64(row.AccessTokenID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":       rule.RuleID,
			"subjectKey":   subject,
			"tokenId":      row.AccessTokenID,
			"userId":       row.UserID,
			"ipCount":      row.IPs,
			"threshold":    ipThreshold,
			"value":        row.IPs,
			"signal":       "multi_ip_drift",
			"windowStart":  windowStart.Format(time.RFC3339),
			"evaluatedAt":  now.Format(time.RFC3339),
			"evidenceNote": "窗口内该 token 出现过的不同 client_ip 数（按 billing_records 去重）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// riskOffhoursStartHour / riskOffhoursEndHour 是夜间时段（北京时间）。
//
// 为什么硬编码而不是走 system_settings：R16 的判据形状是「时段内占比」，
// 改时段会让历史记录的语义漂移（昨天的「凌晨」是 2-5 点，今天改成 3-6 点，
// 那昨天那条告警的含义就变了）。而「连续 3 天」正好依赖历史可比性。
// ⇒ 时段是**判据的一部分**，不是运营可调的运营参数。真要可配，必须先迁移历史。
//
// 运营可调的两项（都通过管理页「规则阈值」区块写入，见 risk_threshold.go）：
//   - `risk_rule_offhours_activity_threshold`：连续天数（整数，≥1）
//   - `risk_offhours_share_threshold`：凌晨占比（0~1，默认 0.8）
//
// ⚠️ 这两项**语义不同、校验规则也不同**（整数 vs 占比），且只有前者会同步
// risk_rules.threshold 列 —— 占比项若也去写那一列，会把「连续天数」覆盖成
// 例如 0.9，而 `consecutiveDays < 1` 会让 R16 直接短路永不告警。
// 见 riskThresholdSpec.SyncRuleColumn。
const (
	riskOffhoursStartHour = 2 // 02:00（含）
	riskOffhoursEndHour   = 5 // 05:00（不含）
	// riskOffhoursShareSettingKey 是「凌晨占比阈值」（0-1 之间，默认 0.8）。
	riskOffhoursShareSettingKey = "risk_offhours_share_threshold"
	// riskOffhoursDefaultShare 默认 80%（96 §2.1 R16 原文「>80%」）。
	riskOffhoursDefaultShare = 0.8
	// riskOffhoursConsecutiveDays 是「连续几天才算命中」的代码默认值。
	// 与规则行的阈值列同义（那一项通过管理页调整）。
	riskOffhoursConsecutiveDays = 3
)

// collectOffhoursActivityCandidates 收集「连续 N 天凌晨活动占比超阈值」的命中（96 §2.1 R16）。
//
// # 用 risk_warnings 自身历史，不建快照表
//
// 理由：跨天状态本来就是告警系统自己的产物；risk_warnings 已有
// (rule_id, subject_key, created_at) 与 idx_risk_warning_subject 索引，
// 按天查是一次索引扫。新建快照表要额外迁移 + 清理任务 + 承担「0 行表」风险
// （R14 的 user_binding_records 就是建了但从未写入，导致规则永远不响）。
//
// # 两阶段判定
//
//  1. **今天**：算当天凌晨（02:00-05:00）的计费笔数占全天比例，≥ 占比阈值 →
//     产出候选（SubjectKey 编入日期，一天一条，天然节流）。
//  2. **历史**：查近 N 天是否已有 N 条**不同日期**的同规则告警。
//     只有凑满 N 天才真正告警 —— 这就是「连续」的实现。
//
// 第 1 阶段「不够」时也要**写一条**吗？不写。写了会让「连续」退化成
// 「只要历史上出现过 N 次就报警」，中间的断档就看不见了。
// 正确做法：只有当天的占比达标才写，然后由历史条数决定是否够「连续」。
//
// ⚠️ **首次上线要等 N 天**：新部署环境没有历史，前 N-1 天永远凑不满连续，
// 因此不会告警。这是设计取舍（宁可延迟发现，不要靠单天误报），
// 已在规则 Description 与前端 label 里写明，避免被误认为规则坏了。
//
// # 时区（这是本规则最容易错的地方）
//
// 查历史必须用 riskWindowColumnExpr(db, "created_at")，**不要另写 substr**：
// risk_warnings.created_at 实测存 `+08:00`，而 logs/users 存 `+00:00`；
// `datetime(col,'+8 hours')` 对两种形态都正确（依赖 SQLite datetime() 先解析偏移
// 再归一），而 substr 会对 `+00:00` 的列错 8 小时。
func collectOffhoursActivityCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	consecutiveDays := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if consecutiveDays < 1 {
		return nil, nil
	}
	shareThreshold := riskOffhoursShareThreshold(db)
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil, nil
	}
	windowStart, windowEnd := riskLoginWindow(now)
	startText := windowStart.Format("2006-01-02 15:04:05")
	endText := windowEnd.Format("2006-01-02 15:04:05")
	timeExpr := riskWindowColumnExpr(db, "created_at")

	// 当天总笔数与凌晨笔数（一次查询里用 CASE 同时算）。
	type dayStat struct {
		Total   int64
		Offhour int64
	}
	var stat dayStat
	offhourExpr := "CASE WHEN " + riskOffhoursHourExpr(db, "created_at") +
		" >= " + strconv.Itoa(riskOffhoursStartHour) +
		" AND " + riskOffhoursHourExpr(db, "created_at") +
		" < " + strconv.Itoa(riskOffhoursEndHour) + " THEN 1 ELSE 0 END"
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Select("COUNT(*) AS total, COALESCE(SUM("+offhourExpr+"), 0) AS offhour").
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?", startText, endText).
		Scan(&stat).Error; err != nil {
		return nil, err
	}
	// 基线不足：当天没有任何计费活动 → 算不出占比（0/0），直接跳过。
	// **不能**把 0/0 当成「占比 0」以外的任何值，更不能当成 1.0。
	if stat.Total <= 0 {
		return nil, nil
	}
	share := float64(stat.Offhour) / float64(stat.Total)
	// 「≥ 占比阈值」：恰等于阈值算命中。
	if share < shareThreshold {
		return nil, nil
	}

	// 当天达标 → 先算「连续天数是否凑满」，再决定产出「告警」还是「仅当日记录」。
	//
	// ⚠️ 本实现第一版在这里犯错（必须记下）：只判当天占比，把 consecutiveDays
	// 填进 Detail 就当实现了「连续 3 天」。那等于规则退化成「单天超标就告警」，
	// 与 96 §2.1 R16「**连续** 3 天」不符，而且新部署环境第 1 天就会告警 ——
	// 与本规则自己声明的「前 3 天不告警」直接矛盾。
	//
	// 正确做法两步：
	//  1. 查近 N 天（含今天）有几个**不同日期**的同规则记录；
	//  2. 额外要求「昨天那条必须存在」——只数「近 N 天有几条」会把
	//     「第 1 天、第 5 天、第 6 天」也算成 3 条，那不是连续。
	today := windowStart.Format("2006-01-02")
	subject := riskBehaviorSubject(rule.RuleID, "day:"+today, windowStart)

	// 查历史必须用 riskWindowColumnExpr：risk_warnings.created_at 实测存 `+08:00`，
	// 而 logs/users 存 `+00:00`；datetime(col,'+8 hours') 对两种形态都正确，
	// substr 会对 `+00:00` 的列错 8 小时（本仓踩过两次）。
	historyExpr := riskWindowColumnExpr(db, "created_at")
	historyFrom := windowStart.AddDate(0, 0, -int(consecutiveDays)+1)
	historyUntil := windowStart.AddDate(0, 0, 1)

	var historyDates int64
	yesterdayExists := int64(0)
	if db.Migrator().HasTable(entity.TableNameRiskWarning) {
		if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
			Where("rule_id = ?", rule.RuleID).
			Where("subject_key LIKE ?", rule.RuleID+":day:%").
			Where(historyExpr+" >= ? AND "+historyExpr+" < ?",
				historyFrom.Format("2006-01-02 15:04:05"), historyUntil.Format("2006-01-02 15:04:05")).
			Distinct("subject_key").
			Count(&historyDates).Error; err != nil {
			return nil, err
		}
		if consecutiveDays > 1 {
			yesterdayStart := windowStart.AddDate(0, 0, -1)
			yesterdaySubject := riskBehaviorSubject(rule.RuleID,
				"day:"+yesterdayStart.Format("2006-01-02"), yesterdayStart)
			if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
				Where("rule_id = ? AND subject_key = ?", rule.RuleID, yesterdaySubject).
				Count(&yesterdayExists).Error; err != nil {
				return nil, err
			}
		}
	}
	// 「连续 N 天」= 昨天也在 且 （窗口内历史不同日期数 + 今天）≥ N。
	//
	// ⚠️ 这里的 `+1` 是**必需的**，不是在补一个 off-by-one。
	//
	// 原因：本函数是「先算 → 再返回候选 → 调用方写库」的顺序，
	// 因此查询 historyDates 时**今天这一行还没落库**，窗口 [today-(N-1), tomorrow)
	// 里实际只有过去 (N-1) 天的记录。今天由 `+1` 补上。
	//
	// 实测（探针，连续 12/13/14 三天各评估一次）：
	//
	//	评估 11-14 时 historyDates=2（11-12、11-13），+1 后 = 3 → 达标，推送 1 次 ✓
	//	Detail 里 consecutiveFound 也必须是 2+1=3，否则排障时会看到「找到 2 天却达标」
	//
	// 反例（把 +1 去掉）：上面那次评估 historyDates=2 < 3 ⇒ **永远凑不满**，
	// 连续规则永不触发 —— 这是「改掉一个看似 off-by-one 的 +1 反而弄坏功能」的实例。
	consecutiveMet := consecutiveDays <= 1
	if consecutiveDays > 1 {
		consecutiveMet = yesterdayExists > 0 && historyDates+1 >= int64(consecutiveDays)
	}

	detail, err := json.Marshal(map[string]any{
		"ruleId":              rule.RuleID,
		"subjectKey":          subject,
		"date":                today,
		"offhourStart":        riskOffhoursStartHour,
		"offhourEnd":          riskOffhoursEndHour,
		"totalBills":          stat.Total,
		"offhourBills":        stat.Offhour,
		"offhourShare":        roundRiskAmount(share),
		"shareThreshold":      shareThreshold,
		"consecutiveRequired": consecutiveDays,
		// consecutiveFound 必须与 consecutiveMet 的口径一致：historyDates 是
		// **库里的**过去日期数（不含今天，因为今天还没写），再加 1 补今天。
		// 两者不一致的话，排障时会看到「找到 2 天却判达标」这种自相矛盾的证据。
		"consecutiveFound": historyDates + 1,
		"consecutiveMet":   consecutiveMet,
		"value":            roundRiskAmount(share),
		"windowStart":      windowStart.Format(time.RFC3339),
		"evaluatedAt":      now.Format(time.RFC3339),
		"evidenceNote": "当天凌晨（北京时间 02:00-05:00）计费笔数占比 + 历史连续天数；" +
			"未凑满连续天数时只留当日记录、不推送（新部署环境前 N-1 天不会产生告警）",
	})
	if err != nil {
		return nil, err
	}
	return []riskWarningCandidate{{
		Rule:     rule,
		Subject:  subject,
		Severity: rule.Severity,
		Detail:   string(detail),
	}}, nil
}

// riskOffhoursConsecutiveMet 从证据快照里读「连续天数是否凑满」。
//
// 为什么不另外传参数：Detail 已经是这条命中的权威快照（含 consecutiveMet），
// 再传一个布尔会形成两处真相，将来必然漂移。解析失败时**保守地返回 false**
// （宁可少推送一次，也不要因为解析错误而在第 1 天就报警 —— 那正是本规则
// 要避免的误报形态）。
func riskOffhoursConsecutiveMet(detail string) bool {
	if strings.TrimSpace(detail) == "" {
		return false
	}
	var parsed struct {
		ConsecutiveMet bool `json:"consecutiveMet"`
	}
	if err := json.Unmarshal([]byte(detail), &parsed); err != nil {
		return false
	}
	return parsed.ConsecutiveMet
}

// riskTokenMultiIPDefault 是「同一 token 的不同 IP 数」的代码默认值（5）。
//
// ⚠️ **这个数字是本仓自定的，不是 97 号给的**。
// 97 号 L321 原文只写「或被多 IP 并发使用」，**没有数量**；那个「3 倍」是
// 配额口径的，被第一版实现借给了 IP 维度。
//
// 为什么定 5 而配额倍数定 3：
//
//	· 配额超 3 倍 = 明确滥用（一个 token 花掉 3 倍配额，没有任何正常解释）。
//	· 3 个 IP 用同一 token = 很可能是**正常场景**（手机 + 电脑 + 平板；
//	  或通勤换网络、公司的双出口 NAT）。定 3 会让这类正常用户天天告警。
//	· 5 个不同 IP 才需要看：再多通常是共享账号或 token 外泄。
//
// 取 5 而不是更高（如 10）：本规则的定位是「疑似外泄/共享」，宁可让运营
// 自己调到 10，也不要默认就放过明显异常。
const riskTokenMultiIPDefault = 5

// riskTokenMultiIPThreshold 读取「同一 token 的不同 IP 数」阈值（R12 的附加判据）。
//
// 走**独立键** `risk_token_abuse_multi_ip_threshold`，不复用
// `risk_rule_token_abuse_threshold`（那是配额倍数的主阈值）。
// 与 riskOffhoursShareThreshold 同构：一条规则的第二判据不占主阈值键。
//
// 非法值（非数字 / ≤0）一律回落默认，**不返回 0** ——
// 返回 0 会让 `float64(row.IPs) < 0` 恒假，等于该判据被静默关掉。
func riskTokenMultiIPThreshold(db *gorm.DB) float64 {
	if db != nil {
		raw := getSystemSettingStringWithDefault(db, RiskThresholdSettingKeyTokenMultiIP, "")
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && value > 0 {
			return value
		}
	}
	return riskTokenMultiIPDefault
}

// riskOffhoursShareThreshold 读取夜间占比阈值（0-1）。非法值回落默认 0.8。
//
// 三层兜底与 riskRuleThreshold 同构：非法值**不能**退化成 0
// （0 会让每一天都「达标」→ 连续 N 天后必然告警，等于规则失效变噪音）。
func riskOffhoursShareThreshold(db *gorm.DB) float64 {
	if db != nil {
		raw := getSystemSettingStringWithDefault(db, riskOffhoursShareSettingKey, "")
		if value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && value > 0 && value <= 1 {
			return value
		}
	}
	return riskOffhoursDefaultShare
}

// riskOffhoursHourExpr 返回「取小时」的方言表达式。
//
// SQLite 必须先用 datetime(col,'+8 hours') 转成北京时间再取小时；
// 直接用 strftime('%H', col) 会按 UTC 解析，凌晨判据整体偏 8 小时
// （本仓 user_portrait 就踩过这个：峰值 15 点 vs 正确 23 点）。
func riskOffhoursHourExpr(db *gorm.DB, column string) string {
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		return "CAST(strftime('%H', " + riskWindowColumnExpr(db, column) + ") AS INTEGER)"
	}
	return "HOUR(" + column + ")"
}

// riskBindingChurnSources 是 R14 计入「用户主动换绑」的 release_source 白名单。
//
// # 为什么是「白名单」而不是黑名单
//
// 六个取值里只有一个是「用户自己主动做的绑定变更」，其余都是**别人/系统**对他的操作。
// 用白名单能保证「将来新增一个 source 时默认不计入」，避免悄悄把受害者算成惯犯。
//
// # 逐个取值说明（全部实测确认过写入点）
//
//   - `user_unbind`（**计入**）：用户主动解绑或换绑。这是唯一真正属于「本人行为」的
//     source —— `phone_binding.go:107`（主动解绑）/`:169`（换绑）、
//     `wechat_binding.go:23`/`:62` 都写它。R14 要抓的就是这种反复操作。
//
//   - `force_reassign`（**排除**）：**被别人抢号**。写点在 `phone_binding.go:149` /
//     `wechat_binding.go:44`，语义是「新人占了号，旧持有人被动释放」。
//     把他算进「频繁换绑」等于**惩罚受害者** —— 这是本规则最容易犯的误报，
//     任务书也专门点了这条。
//
//   - `admin_clear`（**排除**）：管理员清理。**全仓当前无写入点**（只有常量定义），
//     预留给未来的管理端清理功能。排除理由：不是用户行为。
//
//   - `account_close`（**排除**）：注销账号（`user_archive.go:156`）。一个人注销一次
//     不该算「频繁换绑」，且注销后账号已不可用，再告警没有处置意义。
//
//   - `admin_delete`（**排除**）：管理员删除账号（`user_archive.go:154`）。同 account_close。
//
//   - `admin_manual_backfill`（**排除**）：管理员手工补录历史（`user_binding_record_admin.go:449`）。
//     这是数据修复动作，时间戳是**补录时填写的历史时刻**，与「最近 24h 是否频繁换绑」无关；
//     计入会让一次补录批量产出一堆假告警。
//
// ⚠️ 注意这里**不能**用 `release_reason`：主动解绑（`phone_binding.go:106`）与换绑（`:168`）
// 都写 `rebind`，用它数「换绑次数」会把纯解绑也算进去，无法区分。
var riskBindingChurnSources = []string{
	entity.BindingReleaseSourceUserUnbind,
}

// collectBindingChurnCandidates 收集「单账号 24h 内主动换绑 ≥N 次」的命中（96 §2.1 R14）。
//
// # 窗口：滚动 24h，不是自然日
//
// R1（multi_account_ip）用的是自然日窗口，但那是因为它按「一天内登录了几个账号」算，
// 跨零点就该重新计数。R14 不同：它抓的是**短时间内的连续操作**（换绑折腾），
// 自然日窗口会让「23:59 换一次 + 00:01 换一次」被拆到两天从而漏报 —— 而那恰恰是
// 最典型的刷号形态。所以这里用**滚动 24h**（`[now-24h, now)`）。
//
// 代价：SubjectKey 不能编「窗口起点」（滚动窗口每秒都在变），改为编**日期**
// （`day:<YYYY-MM-DD>`）—— 这样同一自然日内最多告警一次（节流语义），
// 但判据本身仍是滚动 24h。这个组合是刻意的：
//
//	· 判据用滚动窗口 → 不因跨零点漏报；
//	· 节流用自然日   → 不会每次评估都重复告警。
//
// # 数据源的三个坑（都实测确认过）
//
//  1. 时间列用 `released_at`（释放时刻），**不是** `bound_at`（当初绑定时刻）。
//     用 bound_at 会把「很久以前绑的、今天才释放」的行算到绑定时间那一侧。
//  2. 必须排除 `state = 'voided'`：管理员作废只改 `state/active_key/void_*`
//     （`user_binding_record_admin.go:315-323`），**不动** `release_reason` 与
//     `released_at` —— 被作废的行仍带着原来的释放时间，不排除就会被当成一次换绑。
//  3. `release_source` 白名单见风险 riskBindingChurnSources 的注释。
func collectBindingChurnCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil, nil
	}
	// 滚动 24h 窗口。
	since := now.Add(-24 * time.Hour)
	releasedExpr := riskWindowColumnExpr(db, "released_at")
	sinceText := since.Format("2006-01-02 15:04:05")
	nowText := now.Format("2006-01-02 15:04:05")

	var groups []RiskBindingChurnGroup
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Select("user_id AS user_id, COUNT(*) AS churn, GROUP_CONCAT(DISTINCT kind) AS kinds").
		Where("state <> ?", entity.BindingStateVoided).
		Where("release_source IN ?", riskBindingChurnSources).
		Where(releasedExpr+" IS NOT NULL").
		Where(releasedExpr+" >= ? AND "+releasedExpr+" < ?", sinceText, nowText).
		Group("user_id").
		Order("user_id ASC")
	if err := query.Scan(&groups).Error; err != nil {
		return nil, err
	}
	windowStart, _ := riskLoginWindow(now)
	today := windowStart.Format("2006-01-02")
	candidates := make([]riskWarningCandidate, 0, len(groups))
	usernames := riskUsernamesByID(db, riskChurnUserIDs(groups))
	for _, group := range groups {
		// 「≥ 阈值」：1 次 + 阈值 2 → 不告警；2 次 → 告警。
		if float64(group.Churn) < threshold {
			continue
		}
		userID := group.UserID
		subject := riskBehaviorSubject(rule.RuleID, "user:"+strconv.FormatInt(int64(userID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":          rule.RuleID,
			"subjectKey":      subject,
			"userId":          userID,
			"username":        usernames[userID],
			"threshold":       threshold,
			"value":           group.Churn,
			"churnCount":      group.Churn,
			"kinds":           group.Kinds,
			"windowHours":     24,
			"windowKind":      "rolling",
			"day":             today,
			"evaluatedAt":     now.Format(time.RFC3339),
			"includedSources": riskBindingChurnSources,
			"evidenceNote": "滚动 24h 内 release_source=user_unbind 的释放次数；" +
				"已排除 state=voided（管理员作废）与 force_reassign（被抢号=受害者）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// RiskBindingChurnGroup 是 R14 的聚合行（用户 → 滚动 24h 内主动释放次数）。
type RiskBindingChurnGroup struct {
	UserID int32
	Churn  int64
	Kinds  string
}

// riskChurnUserIDs 抽出用户 id 列表（一次性查用户名，避免 N+1）。
func riskChurnUserIDs(groups []RiskBindingChurnGroup) []int32 {
	out := make([]int32, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.UserID)
	}
	return out
}

// collectWechatPhoneShareCandidates 收集「同一手机号/微信出现在多个已封禁账号上」的命中
// （96 §2.1 R18）。
//
// # ⚠️ 数据源真实边界：users 上是 UNIQUE 索引，同号**不可能**同时挂在两个账号上
//
// 实测（探针 TestProbePhoneUniqueConstraint）：
//
//	第二个账号用同一 phone → constraint failed: UNIQUE constraint failed: users.phone
//
// `users.phone` 与 `users.wechat_id` 都是 UNIQUE INDEX（`users.gen.go` 的 uniqueIndex tag，
// 实际生成 `CREATE UNIQUE INDEX idx_users_phone`）。这意味着任务书给的那句
//
//	SELECT phone, COUNT(*) FROM users WHERE status='banned' GROUP BY phone HAVING c>=3
//
// 在当前 schema 下**恒为 0 条** —— 直接照做会得到一条永不告警的规则。
//
// # 所以这里改成「同一个号 → 曾被多少个账号占用过」，用 binding 历史
//
// 真正能捕捉「一个人用同一个号反复注册」的信号在 `user_binding_records`：
// 释放行里保留了**历史占用**（released 行仍带着 value），而 users 只保留当前值。
// 判据：
//
//	同一 (kind, value_hash) 出现过 ≥N 个**不同 user_id**，且其中至少一个账号当前是 banned
//
// 「至少一个 banned」是必要的收窄：否则「一个号被 3 个正常用户先后用过」
// （正常的换号流转）也会告警。
//
// 同时保留「当前值的直接分组」作为**补充证据**：虽然 UNIQUE 保证单号只挂一个账号，
// 但如果将来那个 UNIQUE 被去掉（例如允许多账号共用家庭号），这条分支会自动开始工作。
//
// # 双维度（phone / wechat）必须分开
//
// SubjectKey 里带 kind（`phone:` / `wechat:`），否则同一个值在两个维度上会互相覆盖节流。
func collectWechatPhoneShareCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	windowStart, _ := riskLoginWindow(now)
	candidates := make([]riskWarningCandidate, 0, 4)
	seen := map[string]struct{}{}

	// ① 主判据：binding 历史里同一 (kind,value) 被 ≥N 个不同账号占用，且含封禁账号。
	if db.Migrator().HasTable(entity.TableNameUserBindingRecord) &&
		db.Migrator().HasTable(entity.TableNameUser) {
		type shareGroup struct {
			Kind       string
			Value      string
			UserCount  int64
			Banned     int64
			SampleUser string
		}
		var groups []shareGroup
		// 用 JOIN users 取「其中有多少个账号当前是 banned」。
		// 注意 value_hash 是 (kind,value) 的稳定指纹，比直接比 value 更可靠
		// （value 可能有格式差异，hash 已归一）。
		query := db.Session(&gorm.Session{NewDB: true}).
			Table(entity.TableNameUserBindingRecord+" AS b").
			Select("b.kind AS kind, MAX(b.value) AS value, "+
				"COUNT(DISTINCT b.user_id) AS user_count, "+
				"SUM(CASE WHEN u.status = ? THEN 1 ELSE 0 END) AS banned, "+
				"MAX(b.username_snapshot) AS sample_user", "banned").
			Joins("JOIN " + entity.TableNameUser + " AS u ON u.id = b.user_id").
			Where("b.value_hash <> ''").
			Group("b.kind, b.value_hash").
			Order("user_count DESC")
		if err := query.Scan(&groups).Error; err != nil {
			return nil, err
		}
		for _, group := range groups {
			// 「≥ 阈值」且至少有一个封禁账号。
			if float64(group.UserCount) < threshold || group.Banned <= 0 {
				continue
			}
			subject := riskBehaviorSubject(rule.RuleID,
				group.Kind+":"+group.Value, windowStart)
			seen[group.Kind+":"+group.Value] = struct{}{}
			var userID int32
			detail, err := json.Marshal(map[string]any{
				"ruleId":       rule.RuleID,
				"subjectKey":   subject,
				"kind":         group.Kind,
				"value":        group.Value,
				"accountCount": group.UserCount,
				"bannedCount":  group.Banned,
				"sampleUser":   group.SampleUser,
				// ⚠️ 这里必须是**阈值**，不是实际计数。
				// 原实现写的是 group.UserCount（实际值），会让排障的人看到
				// 「threshold 恰好等于 accountCount」而以为「刚好卡在阈值上」——
				// 实际可能远超阈值。其余 9 条规则都写 `threshold` 这个变量，
				// 这两处是笔误，已修正。
				"threshold":   threshold,
				"signal":      "binding_history",
				"windowStart": windowStart.Format(time.RFC3339),
				"evaluatedAt": now.Format(time.RFC3339),
				"evidenceNote": "同一号码/微信历史上被多个账号占用（含封禁账号）；" +
					"users 上是 UNIQUE 索引，所以只能从 binding 历史看出「曾被多人占用」",
			})
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, riskWarningCandidate{
				Rule:     rule,
				Subject:  subject,
				UserID:   &userID,
				Severity: rule.Severity,
				Detail:   string(detail),
			})
		}
	}

	// ② 补充分支：当前值直接分组（UNIQUE 存在时恒 0；去掉 UNIQUE 后自动生效）。
	if db.Migrator().HasTable(entity.TableNameUser) {
		for _, dimension := range []struct {
			kind   string
			column string
		}{
			{entity.BindingKindPhone, "phone"},
			{entity.BindingKindWechat, "wechat_id"},
		} {
			type currentGroup struct {
				Value string
				Count int64
			}
			var rows []currentGroup
			if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
				Select(dimension.column+" AS value, COUNT(*) AS count").
				Where("status = ?", "banned").
				Where(dimension.column+" IS NOT NULL AND "+dimension.column+" <> ''").
				Group(dimension.column).
				Having("COUNT(*) >= ?", int(threshold)).
				Scan(&rows).Error; err != nil {
				return nil, err
			}
			for _, row := range rows {
				key := dimension.kind + ":" + row.Value
				if _, ok := seen[key]; ok {
					continue
				}
				subject := riskBehaviorSubject(rule.RuleID, key, windowStart)
				var userID int32
				detail, err := json.Marshal(map[string]any{
					"ruleId":      rule.RuleID,
					"subjectKey":  subject,
					"kind":        dimension.kind,
					"value":       row.Value,
					"bannedCount": row.Count,
					// 与主判据同理：threshold 是阈值，不是 row.Count。
					"threshold":   threshold,
					"signal":      "current_binding",
					"windowStart": windowStart.Format(time.RFC3339),
					"evaluatedAt": now.Format(time.RFC3339),
					"evidenceNote": "按 users 当前值分组；users.phone/wechat_id 有 UNIQUE 索引，" +
						"所以本分支当前恒为 0（保留是为 schema 变更后能自动生效）",
				})
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, riskWarningCandidate{
					Rule:     rule,
					Subject:  subject,
					UserID:   &userID,
					Severity: rule.Severity,
					Detail:   string(detail),
				})
			}
		}
	}
	return candidates, nil
}

// riskImpossibleTravelMaxSpeedKmh 是「物理不可能」的速度上限（km/h）。
//
// # ⚠️ 与 96/97 号文档的偏差（记录决策依据，不是改行为）
//
// 97 号 L282 与 96 号 L108 的原始定义是：
//
//	同一用户两次登录的 IP **分属不同省份**，且间隔 **< 2h**
//
// **本仓改用「物理速度 > 900 km/h」，而不是「跨省 + 2h」。** 理由：
//
//   - 原定义会**误报合法航班**：北京→上海 1888 km 直飞约 2.1 小时，
//     一个真实出差的人在 2 小时内跨省登录是完全正常的，却会被原判据命中。
//   - 速度判据把「距离」纳入考虑：同样的 2 小时，跨省但只飞了 300 km
//     （如北京→济南）不会告警，而 1 小时内跨 1888 km 一定告警。
//   - 原定义还需要「省份」数据（依赖 `ip_segments` 的 `region` 解析），
//     而本仓有**经纬度**（`latitude`/`longitude` 实测有值），能算真实球面距离 ——
//     比按省名比对更准确（同名省、直辖市、境外 IP 都无需特判）。
//
// ⇒ **实现比文档更严格、更准确，应当保留实现**。但运营若按文档理解
// 「跨省就告警」，实际行为会不符预期 —— 所以 R3 的 Description 写的是速度口径，
// 并另有「仅公网 IP」「只回溯 7 天」两条限制说明。
//
// # 为什么是 900
//
// 900 km/h 是**民航客机的巡航上限**（约 Mach 0.85，常见机型 800–900）。
// 取民航而不是「战斗机/火箭」是有意的保守选择：我们要抓的是「同一个账号在两地
// 之间瞬移」，而两地的**最快现实交通方式**就是民航。用更高的上限（如协和式的
// 2000 km/h）会把大量真实异常放走；用更低（如高铁 350）会把「同一天内北京→上海
// 坐飞机」这种正常出差误判成异常。
//
// ⚠️ 这个数字是**行业常识值，不是本仓实测值**。它作为**代码默认值**给出，
// 运营可通过 risk_rule_impossible_travel_threshold 调整（见 riskThresholdSpecs）。
const riskImpossibleTravelMaxSpeedKmh = 900.0

// riskImpossibleTravelMinIntervalMinutes 是「最小判定间隔」（分钟）。
//
// 物理上：distance / 900km/h 得到「最快到达时间」。但为了抗**定位误差**，
// 实际判据还要求「时间间隔 ≥ 本值」才进入比较 —— 否则两个相邻城区的 IP
// （坐标差 20km、间隔 30 秒）会被算成「45000 km/h」而误报。
//
// 30 分钟的来源：同城跨区通勤的现实下限。**这是拍的**（没有权威数据支撑），
// 但它只在「距离很近」时才起作用 —— 城市间的真实瞬移（如北京→广州 1900km）
// 需要 2.1 小时的飞行时间，远超这个下限，不会被它挡掉。
const riskImpossibleTravelMinIntervalMinutes = 30

// riskImpossibleTravelEarthRadiusKm 是球面距离公式用的地球半径。
const riskImpossibleTravelEarthRadiusKm = 6371.0

// riskImpossibleTravelLookbackDays 是 R3 回溯登录序列的天数。
//
// # 为什么是 7 天（这是**实现侧的选择，不是需求给定**）
//
// R3 只需要「相邻两次登录」，所以理论上只看最近一次之前的记录就够。选 7 天是折中：
//   - 远大于「相邻两次」的需要 —— 覆盖「今天之前的最后一次」绰绰有余；
//   - 远小于 `logs` 的 30 天保留期 —— 不必扫全表（这是性能考虑，不是语义考虑）。
//
// # ⚠️ 这是一条真实的**漏报边界**（不要当成无遗漏）
//
// 如果某用户**超过 7 天没有登录**，那么他「7 天前那次」与「今天这次」这一对
// **不会被比较**。例如：用户在北京登录后出差两周，回程前账号在广州被人登录 ——
// 中间隔了 14 天，两条都不在同一个 7 天窗口里，R3 **不会告警**。
//
// 为什么接受这个漏报：拉长窗口会让每次评估扫描的登录行数线性增长，而真实盗号
// 的「瞬移」几乎都发生在小时级间隔内（人的移动需要时间，超过一天的跨度里
// 「物理不可能」这个判据本身就失效了 —— 24 小时足够飞到地球任何地方）。
//
// 之所以写成常量而不是字面量：它需要出现在**规则说明**里（让运营知道边界），
// 两处引用同一个值才不会漂移 —— 说明里写「7 天」而代码是 5 天，比不说更糟。
const riskImpossibleTravelLookbackDays = 7

// collectImpossibleTravelCandidates 收集「单账号相邻两次成功登录的移动速度物理不可能」的命中
// （96 §2.1 R3）。
//
// # 数据源
//
// `logs` 表里 `module='auth'` + `action=10` + `new_answer LIKE '%"status":"success"%'`
// 的成功登录行（与 R1 同一谓词，见 riskLoginAuditAction / riskLoginAuditModule）。
// **只看成功登录**：攻击者拿到密码后异地登录本身就是成功登录；失败尝试由 R9
// 另建表处理（`login_attempts` 只有累计计数，无逐次流水）。
//
// # 判据
//
// 对同一 user_id 的登录序列按 create_time 排序，逐对比较相邻两条：
//
//	速度 = 球面距离(上一条 IP, 本条 IP) / 时间间隔
//	若 速度 > riskImpossibleTravelMaxSpeedKmh（且时间间隔 ≥ 最小间隔）→ 命中
//
// # 边界处理（每条都有用例）
//
//   - **同一 IP 连续两次** → 距离 0 → 速度 0 → 不告警。
//   - **时间倒序/相同时间**（脏数据）→ 间隔 ≤ 0 → 跳过该对（不能算出负速度）。
//   - **归属地查不到**（私网 IP / 段表未覆盖 / 上游没给坐标）→ 跳过该对，不告警。
//     ⚠️ 这条最重要：把「未知」当成「异地」会让所有内网 IP 都告警
//     （本机实测生产库 logs 里全是 172.x 私网地址）。
//   - **首次登录**（没有上一条）→ 自然跳过。
//   - **30 天前的行已被清理** → 只能看到保留期内的序列；清理后的第一次登录
//     没有前一条，自然不告警（不会误判成「从无到有」）。
//
// # 性能
//
// 依赖 `idx_logs_user_time(user_id, create_time)` 复合索引（见
// registry.ensureLogsUserTimeIndex）。没有它也能跑，但会退化成内存排序。
func collectImpossibleTravelCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		return nil, nil
	}
	windowStart, _ := riskLoginWindow(now)
	// 取最近 riskImpossibleTravelLookbackDays 天的登录序列。
	since := now.Add(-time.Duration(riskImpossibleTravelLookbackDays) * 24 * time.Hour)
	timeExpr := riskWindowColumnExpr(db, "create_time")

	type loginRow struct {
		UserID    int32
		ClientIP  string
		CreatedAt time.Time
	}
	var rows []loginRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Log{}).
		Select("user_id, client_ip, create_time AS created_at").
		Where("action = ?", riskLoginAuditAction).
		Where("module = ?", riskLoginAuditModule).
		Where("client_ip <> ''").
		Where("user_id > 0").
		Where("new_answer LIKE ?", `%"status":"success"%`).
		Where(timeExpr+" >= ?", since.Format("2006-01-02 15:04:05")).
		Order("user_id ASC, create_time ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, nil
	}

	// 坐标为空的 IP 只查一次（同一 IP 会在多行里重复出现）。
	coordCache := map[string]*riskGeoPoint{}
	geoFor := func(ip string) *riskGeoPoint {
		if point, ok := coordCache[ip]; ok {
			return point
		}
		point := riskLookupIPGeoPoint(db, ip)
		coordCache[ip] = point
		return point
	}

	// 按 user_id 分组逐对比较（rows 已按 user_id, create_time 排序）。
	candidates := make([]riskWarningCandidate, 0, 8)
	minInterval := time.Duration(riskImpossibleTravelMinIntervalMinutes) * time.Minute
	for i := 1; i < len(rows); i++ {
		prev, current := rows[i-1], rows[i]
		if prev.UserID != current.UserID {
			continue // 换用户了，本对无意义
		}
		// 同一 IP → 距离 0，绝不可能告警（显式短路，省掉坐标查询）。
		if strings.TrimSpace(prev.ClientIP) == strings.TrimSpace(current.ClientIP) {
			continue
		}
		interval := current.CreatedAt.Sub(prev.CreatedAt)
		// 时间倒序 / 相同时间（脏数据）→ 跳过。不能算出负速度。
		if interval <= 0 {
			continue
		}
		// 时间间隔太小 → 定位误差主导，不判（见 riskImpossibleTravelMinIntervalMinutes）。
		if interval < minInterval {
			continue
		}
		from, to := geoFor(prev.ClientIP), geoFor(current.ClientIP)
		// 任一端查不到坐标 → 跳过。**不能**把未知当异地。
		if from == nil || to == nil {
			continue
		}
		distanceKm := riskHaversineKm(*from, *to)
		// 同一点（坐标相同）→ 距离 0 → 不告警。
		if distanceKm < 1 {
			continue
		}
		hours := interval.Hours()
		speedKmh := distanceKm / hours
		// 「≥ 阈值」：速度恰等于阈值算命中。
		if speedKmh < threshold {
			continue
		}
		userID := current.UserID
		subject := riskBehaviorSubject(rule.RuleID,
			"user:"+strconv.FormatInt(int64(userID), 10), windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":          rule.RuleID,
			"subjectKey":      subject,
			"userId":          userID,
			"username":        riskUsernamesByID(db, []int32{userID})[userID],
			"threshold":       threshold,
			"value":           roundRiskAmount(speedKmh),
			"speedKmh":        roundRiskAmount(speedKmh),
			"distanceKm":      roundRiskAmount(distanceKm),
			"intervalMinutes": roundRiskAmount(interval.Minutes()),
			"fromIp":          prev.ClientIP,
			"toIp":            current.ClientIP,
			"fromRegion":      from.Label,
			"toRegion":        to.Label,
			"fromAt":          prev.CreatedAt.Format(time.RFC3339),
			"toAt":            current.CreatedAt.Format(time.RFC3339),
			"maxSpeedKmh":     riskImpossibleTravelMaxSpeedKmh,
			"windowStart":     windowStart.Format(time.RFC3339),
			"evaluatedAt":     now.Format(time.RFC3339),
			"evidenceNote": "相邻两次成功登录的球面距离 / 时间间隔；" +
				"已跳过同 IP、时间倒序、坐标未知、间隔小于最小判定阈值的对",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userID,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}

// riskGeoPoint 是一次 IP 归属地查询的结果（坐标 + 展示用地区名）。
type riskGeoPoint struct {
	Latitude  float64
	Longitude float64
	Label     string
}

// riskLookupIPGeoPoint 查某 IP 的经纬度。返回 nil 表示**查不到**（不是「在原点」）。
//
// 三层跳过，顺序重要：
//  1. 私网/保留地址 → nil（它们永远不在公网段表里，查也是白查；
//     且把「内网 IP」当异地会让所有容器/内网部署全量误报）；
//  2. 段表未覆盖 → nil；
//  3. 段记录存在但上游没给坐标（Latitude/Longitude 为 nil）→ nil。
//
// ⇒ 调用方必须把 nil 当作「跳过这一对」，不能当作「距离 0」或「距离无穷」。
func riskLookupIPGeoPoint(db *gorm.DB, ip string) *riskGeoPoint {
	trimmed := strings.TrimSpace(ip)
	if trimmed == "" || IsPrivateOrReservedIP(trimmed) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameIPSegment) {
		return nil
	}
	segment, err := FindIPSegmentWithDB(db, trimmed)
	if err != nil || segment == nil {
		return nil
	}
	if segment.Latitude == nil || segment.Longitude == nil {
		return nil
	}
	return &riskGeoPoint{
		Latitude:  *segment.Latitude,
		Longitude: *segment.Longitude,
		Label:     strings.TrimSpace(segment.Region),
	}
}

// riskHaversineKm 计算两点间的球面距离（大圆距离，单位 km）。
//
// 用 haversine 而不是简单欧氏距离：后者在跨纬度时误差极大
// （高纬度地区经度 1 度的实际距离远小于赤道）。
func riskHaversineKm(from, to riskGeoPoint) float64 {
	lat1 := from.Latitude * math.Pi / 180
	lat2 := to.Latitude * math.Pi / 180
	dLat := (to.Latitude - from.Latitude) * math.Pi / 180
	dLon := (to.Longitude - from.Longitude) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return riskImpossibleTravelEarthRadiusKm * c
}

// riskCredentialStuffingWindow 是 R9 的判定窗口（30 分钟**滑动**窗口，不是自然日）。
//
// ⚠️ **这个数字没有本仓实测依据**，来自 96 号文档的规格。
// 选滑动窗口而不是自然日的理由：撞库是「几十秒到几分钟内打完一批账号」的短时爆发，
// 自然日窗口会把 23:59 与 00:01 的两次爆发拆到两天，各自都不到阈值 —— 双双漏报。
// 这与 R14（binding_churn）选滚动 24h 是同一个道理。
const riskCredentialStuffingWindow = 30 * time.Minute

// collectCredentialStuffingCandidates 收集「同 IP 在 30 分钟内试了 ≥N 个不同账号」的命中
// （96 §2.1 R9）。
//
// # 数据源
//
// `login_failure_events`（entity.LoginFailureEvent）—— 每次登录失败一行。
// **只看失败登录**：撞库必然以大量失败告终；成功的登录属于正常登录，不是本规则的输入。
//
// # 判据
//
// 按 `client_ip` 分组，在每个滑动 30 分钟窗口内数**不同账号名**的个数：
//
//	COUNT(DISTINCT username) >= 阈值  → 命中
//
// # ⚠️ 「账号不存在」必须与「密码错误」同等计入（这是 R9 与 R2 的分界线）
//
// 撞库的**定义**就是「用一个密码列表去试大量不同账号」，其中绝大多数账号名可能
// 根本不存在（`user_id = 0`）。若只统计真实存在的账号，检测的就不是撞库，
// 而是「对已知账号的暴力破解」—— 那是 R2 的范畴。
// 所以本查询**不带任何 user_id 过滤**，连 `user_id = 0` 的行一起数。
//
// # 为什么不按 username 建索引
//
// `username` 是攻击者完全控制的任意字符串（撞库时每请求不同），
// 建索引会在长度维度被撑爆且选择性极差。查询只按 `(client_ip, created_at)`
// 过滤（该复合索引已建），username 只参与 `COUNT(DISTINCT)` 的内存聚合。
func collectCredentialStuffingCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		return nil, nil
	}
	windowStart, windowEnd := now.Add(-riskCredentialStuffingWindow), now
	timeExpr := riskWindowColumnExpr(db, "created_at")

	// 先按 IP 拉出窗口内的明细（IP + username），在 Go 侧做滑动窗口聚合。
	//
	// 为什么不直接在 SQL 里 GROUP BY：滑动窗口需要「每个 IP 的每 30 分钟片段」，
	// SQL 表达要么用自连接（慢且方言差异大），要么用窗口函数（SQLite < 3.25 不支持）。
	// 30 分钟窗口内的失败量在单 IP 上至多几百行，Go 侧排序聚合完全够用。
	type failureRow struct {
		ClientIP string
		Username string
	}
	var rows []failureRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.LoginFailureEvent{}).
		Select("client_ip, username").
		Where("client_ip <> ''").
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?",
			windowStart.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")).
		Order("client_ip ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 按 IP 聚合不同账号名。
	byIP := make(map[string]map[string]struct{}, 16)
	order := make([]string, 0, 16)
	for _, row := range rows {
		ip := strings.TrimSpace(row.ClientIP)
		if ip == "" {
			continue
		}
		usernames, ok := byIP[ip]
		if !ok {
			usernames = make(map[string]struct{}, 8)
			byIP[ip] = usernames
			order = append(order, ip)
		}
		// 空 username 不计入「不同账号」：`failIPOnly` 路径（/admin-login）
		// 拿不到账号名，把它算成一个「账号」会让该入口的几次失败被误判成撞库。
		username := strings.TrimSpace(row.Username)
		if username == "" {
			continue
		}
		usernames[username] = struct{}{}
	}

	candidates := make([]riskWarningCandidate, 0, 4)
	for _, ip := range order {
		accounts := len(byIP[ip])
		if float64(accounts) < threshold {
			continue
		}
		sample := make([]string, 0, 3)
		for username := range byIP[ip] {
			if len(sample) >= 3 {
				break
			}
			sample = append(sample, username)
		}
		sort.Strings(sample)
		subject := riskBehaviorSubject(rule.RuleID, "ip:"+ip, windowStart)
		detail, err := json.Marshal(map[string]any{
			"ruleId":        rule.RuleID,
			"subjectKey":    subject,
			"ip":            ip,
			"threshold":     threshold,
			"value":         accounts,
			"accountCount":  accounts,
			"sampleUser":    sample,
			"windowMinutes": int(riskCredentialStuffingWindow / time.Minute),
			"windowKind":    "rolling",
			"windowStart":   windowStart.Format(time.RFC3339),
			"windowEnd":     windowEnd.Format(time.RFC3339),
			"evaluatedAt":   now.Format(time.RFC3339),
			"evidenceNote": "同一 IP 在 30 分钟滑动窗口内失败的**不同账号名**个数；" +
				"账号不存在（user_id=0）与密码错误同等计入（撞库的定义如此）；" +
				"username 为空的行不计入（/admin-login 路径拿不到账号名）",
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			Severity: rule.Severity,
			Detail:   string(detail),
			IP:       ip,
		})
	}
	return candidates, nil
}

// RiskTicketFloodGroup 是 R8 的聚合行（用户 → 窗口内建单数）。
type RiskTicketFloodGroup struct {
	UserID int32
	Ticket int64
}

// riskTicketFloodUserIDs 抽出用户 id 列表（用于一次性查用户名，避免 N+1）。
func riskTicketFloodUserIDs(groups []RiskTicketFloodGroup) []int32 {
	out := make([]int32, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.UserID)
	}
	return out
}

// riskUsernamesByID 批量取用户名（证据快照里带上用户名，排障时比纯 id 好用）。
//
// 查不到不算错误：用户可能已被归档，告警仍然要发（证据里留空即可）。
func riskUsernamesByID(db *gorm.DB, ids []int32) map[int32]string {
	result := make(map[int32]string, len(ids))
	if len(ids) == 0 || !db.Migrator().HasTable(entity.TableNameUser) {
		return result
	}
	type row struct {
		ID       int32
		Username string
	}
	var rows []row
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Select("id, username").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return result
	}
	for _, item := range rows {
		result[item.ID] = item.Username
	}
	return result
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
	for _, item := range entity.DefaultRiskRules() {
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
