package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	riskTestRiskWebhook   = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=risk-secret"
	riskTestTicketWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=ticket-secret"
)

func newRiskAlertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:risk-alert-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// ⚠️ AutoMigrate 必须**逐个模型分次调用**：一次传多个模型时 GORM 会把建表语句
	// 合并，导致物理表列缺失，而报错信息完全不会提到 AutoMigrate（队长广播事故③）。
	//
	// TicketNotification 是 UpdateTicketWecomSettingsWithDB 的联动依赖
	// （关闭/清空工单配置时要把 pending 的 outbox 行置为 skipped），
	// 不建表会让「清工单配置」这一步直接报 no such table。
	for _, model := range []any{
		&entity.SystemSetting{},
		&entity.User{},
		&entity.BillingRecord{},
		&entity.RiskRule{},
		&entity.RiskWarning{},
		&entity.TicketNotification{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedRiskRules 写入 3 条规则（与 registry 的种子同值，避免依赖 registry 包）。
func seedRiskRules(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, item := range entity.DefaultRiskDebtRules {
		row := entity.RiskRule{
			RuleID:          item.RuleID,
			Name:            item.Name,
			Severity:        item.Severity,
			Enabled:         true,
			Threshold:       item.Threshold,
			ThrottleSeconds: item.ThrottleSeconds,
			Description:     item.Description,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed rule %s: %v", item.RuleID, err)
		}
	}
}

func putRiskSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, key, value, "risk-test"); err != nil {
		t.Fatalf("save setting %s: %v", key, err)
	}
}

// riskSenderRecorder 记录所有发信调用（并发安全：告警判定可能并发触发）。
type riskSenderRecorder struct {
	mu     sync.Mutex
	calls  []riskSenderCall
	onCall func(riskSenderCall) error
}

type riskSenderCall struct {
	Config  RiskWecomConfig
	Content string
}

func (r *riskSenderRecorder) install(t *testing.T) {
	t.Helper()
	previous := riskWebhookSender
	riskWebhookSender = func(_ context.Context, config RiskWecomConfig, content string, _ []string) error {
		r.mu.Lock()
		r.calls = append(r.calls, riskSenderCall{Config: config, Content: content})
		handler := r.onCall
		r.mu.Unlock()
		if handler != nil {
			return handler(riskSenderCall{Config: config, Content: content})
		}
		return nil
	}
	t.Cleanup(func() { riskWebhookSender = previous })
}

func (r *riskSenderRecorder) snapshot() []riskSenderCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]riskSenderCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// ============================================================
// C2 —— 「风控 webhook 未配置时绝不回退到工单 webhook」的硬钉子
// ============================================================
//
// 这是 96 §0 C2 第三条验收判据的**唯一**可执行形式：
//
//	只配工单 webhook、风控留空 → 风控告警不发推送（不得借用工单的）
//
// ⚠️ 为什么必须断言「sender 一次都没被调用」，而不是只断言 Notified=false：
//
//	『回退到 ticket webhook』这个错误实现有两条分支：
//	  a) 回退后**发送成功** → Notified 变 true（断言 false 能抓到）
//	  b) 回退后**发送失败**（测试里没有任何真实 HTTP 环境，实际必然失败）
//	     → Notified 仍是 false，只断言 Notified=false 的实现会**静默放过**
//	所以判据必须落在「有没有尝试发」这一层。本用例同时断言 NotifiedAt 为 nil
//	（双保险：即使某天 sender 被 stub 成永远成功，也没留下推送痕迹）。
func TestRiskAlertDoesNotFallBackToTicketWebhook(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 只配工单三件套，风控三件套一个都不配。
	putRiskSetting(t, db, TicketWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, TicketWecomWebhookURLSettingKey, riskTestTicketWebhook)
	putRiskSetting(t, db, TicketWecomMentionedListSettingKey, `["ticket-owner"]`)

	recorder := &riskSenderRecorder{}
	recorder.install(t)

	warning := entity.RiskWarning{
		RuleID:     entity.RiskRuleIDDebtUser,
		SubjectKey: "user:1",
		Severity:   entity.RiskSeverityMedium,
		Detail:     `{"value":150,"threshold":100}`,
		CreatedAt:  time.Now(),
	}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, reason, err := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if pushed {
		t.Fatal("风控 webhook 未配置时绝不允许推送（96 C2）")
	}
	if reason != riskNotifyWebhookEmpty {
		t.Fatalf("reason = %q, want %q（必须区分「未配置」与「被节流」）", reason, riskNotifyWebhookEmpty)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("回退到工单 webhook 了：sender 被调用 %d 次，第一次 config=%+v", len(calls), calls[0].Config)
	}
	// 双保险：即使 sender 被 stub 成永远成功，也不该留下推送痕迹。
	var stored entity.RiskWarning
	if err := db.First(&stored, warning.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Notified {
		t.Fatal("notified must stay false when the risk webhook is not configured")
	}
	if stored.NotifiedAt != nil {
		t.Fatal("notified_at must stay nil when the risk webhook is not configured")
	}
	// 反向断言：工单配置必须**原封不动**（回退实现常伴随「顺手改工单配置」）。
	ticketConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket config: %v", err)
	}
	if ticketConfig.WebhookURL != riskTestTicketWebhook || !ticketConfig.Enabled {
		t.Fatalf("ticket config was mutated by the risk path: %+v", ticketConfig)
	}
}

// 同一枚钉子的第二种形态：风控 enabled=true 但 URL 为空（管理员只打开了开关没填地址）。
//
// 这是真实线上最常见的误配 —— 前端「启用」开关是独立控件，很容易只点开关。
// 此时期望：不推送、不借工单通道、错误原因仍是 webhook_not_configured。
func TestRiskAlertEnabledWithoutWebhookStillDoesNotBorrowTicket(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, TicketWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, TicketWecomWebhookURLSettingKey, riskTestTicketWebhook)

	recorder := &riskSenderRecorder{}
	recorder.install(t)

	warning := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtSystem, SubjectKey: "system:debt", Severity: entity.RiskSeverityHigh, CreatedAt: time.Now()}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, reason, err := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if err != nil || pushed {
		t.Fatalf("pushed=%v err=%v：开关打开但 URL 为空时必须只落库", pushed, err)
	}
	if reason != riskNotifyWebhookEmpty {
		t.Fatalf("reason = %q, want %q", reason, riskNotifyWebhookEmpty)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("sender must not be called at all, got %d calls", len(calls))
	}
}

// 反向判据（96 §0 C3 判据 2）：只配风控、工单留空 → 风控照发，且用的是风控的 URL。
func TestRiskAlertUsesItsOwnWebhookWhenConfigured(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, RiskWecomWebhookURLSettingKey, riskTestRiskWebhook)
	putRiskSetting(t, db, RiskWecomMentionedListSettingKey, `["risk-owner"]`)

	recorder := &riskSenderRecorder{}
	recorder.install(t)

	warning := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtUser, SubjectKey: "user:9", Severity: entity.RiskSeverityMedium, Detail: `{"value":150}`, CreatedAt: time.Now()}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, reason, err := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v, want true", pushed, err)
	}
	if reason != riskNotifySent {
		t.Fatalf("reason = %q, want %q", reason, riskNotifySent)
	}
	calls := recorder.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 send, got %d", len(calls))
	}
	if calls[0].Config.WebhookURL != riskTestRiskWebhook {
		t.Fatalf("sent to %q, want the risk webhook %q", calls[0].Config.WebhookURL, riskTestRiskWebhook)
	}
	if !strings.Contains(calls[0].Content, "风控告警") {
		t.Fatalf("content must be a risk alert, got %q", calls[0].Content)
	}
	// 消息里的跳转链接必须是**风控告警详情**，不是工单详情（96 §4.3）。
	if !strings.Contains(calls[0].Content, riskAlertDetailPathPrefix) {
		t.Fatalf("content must link to the risk alert detail path, got %q", calls[0].Content)
	}
	if strings.Contains(calls[0].Content, "/admin/tickets/") {
		t.Fatal("risk alert must not link to a ticket detail page")
	}
	var stored entity.RiskWarning
	if err := db.First(&stored, warning.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !stored.Notified || stored.NotifiedAt == nil {
		t.Fatalf("successful push must set notified/notified_at: %+v", stored)
	}
}

// 未启用时不推送，但**告警记录仍然落库**（96 §2.4：节流/开关不等于不留痕）。
func TestRiskAlertDisabledStillRecordsButNeverSends(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskWecomEnabledSettingKey, "false")
	putRiskSetting(t, db, RiskWecomWebhookURLSettingKey, riskTestRiskWebhook)

	recorder := &riskSenderRecorder{}
	recorder.install(t)

	warning := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtUser, SubjectKey: "user:3", Severity: entity.RiskSeverityMedium, CreatedAt: time.Now()}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, reason, err := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if err != nil || pushed || reason != riskNotifyDisabled {
		t.Fatalf("pushed=%v reason=%q err=%v", pushed, reason, err)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("disabled config must not send, got %d calls", len(calls))
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("warning row must exist regardless of push outcome, count=%d err=%v", count, err)
	}
}

// ============================================================
// C3 —— 两个输入框：值可相同，记录独立，清除互不影响
// ============================================================

func TestRiskWecomSettingsAreIndependentFromTicketSettings(t *testing.T) {
	db := newRiskAlertTestDB(t)
	enabled := true
	// 两条配置填**同一个 URL**（96 §0 C3 判据 3）。
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: riskTestTicketWebhook}); err != nil {
		t.Fatalf("update ticket settings: %v", err)
	}
	riskSettings, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{Enabled: &enabled, WebhookURL: riskTestTicketWebhook})
	if err != nil {
		t.Fatalf("update risk settings: %v", err)
	}
	if !riskSettings.Configured || !riskSettings.Enabled {
		t.Fatalf("risk settings not saved: %+v", riskSettings)
	}
	// 响应里 URL 恒为空，只有脱敏预览（96 §1.1 的可复用契约）。
	if riskSettings.WebhookURL != "" {
		t.Fatalf("webhook URL must never be echoed, got %q", riskSettings.WebhookURL)
	}
	if riskSettings.Preview != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置" {
		t.Fatalf("unexpected preview: %q", riskSettings.Preview)
	}
	// 两条独立记录：DB 里必须是**两行**同名 key 的不同前缀。
	var keys []string
	if err := db.Model(&entity.SystemSetting{}).Where("key IN ?", []string{
		RiskWecomWebhookURLSettingKey, TicketWecomWebhookURLSettingKey,
	}).Pluck("key", &keys).Error; err != nil {
		t.Fatalf("pluck keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 independent webhook rows, got %v", keys)
	}
	// 清除风控 → 工单那条必须完好（判据 3 的后半句）。
	cleared, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{ClearWebhook: true})
	if err != nil {
		t.Fatalf("clear risk: %v", err)
	}
	if cleared.Configured || cleared.Preview != "" || cleared.WebhookURL != "" {
		t.Fatalf("risk webhook not cleared: %+v", cleared)
	}
	ticketConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if ticketConfig.WebhookURL != riskTestTicketWebhook {
		t.Fatalf("clearing the risk webhook must not touch the ticket webhook, got %q", ticketConfig.WebhookURL)
	}
	// 反向：清工单 → 风控完好。
	riskEnabled := true
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{Enabled: &riskEnabled, WebhookURL: riskTestRiskWebhook}); err != nil {
		t.Fatalf("re-set risk: %v", err)
	}
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{ClearWebhook: true}); err != nil {
		t.Fatalf("clear ticket: %v", err)
	}
	config, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("read risk: %v", err)
	}
	if config.WebhookURL != riskTestRiskWebhook || !config.Enabled {
		t.Fatalf("clearing the ticket webhook must not touch the risk config: %+v", config)
	}
}

// 空白 / 掩码更新 = 保持原值（前端不回显凭据，必须支持「只改开关」）。
func TestRiskWecomSettingsPreserveSecretOnBlankOrMaskedUpdate(t *testing.T) {
	db := newRiskAlertTestDB(t)
	enabled := true
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{
		Enabled: &enabled, WebhookURL: riskTestRiskWebhook, MentionedList: []string{"a", "a", "b"},
	}); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	for _, value := range []string{"", SecretMask, "   "} {
		settings, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{Enabled: &enabled, WebhookURL: value})
		if err != nil {
			t.Fatalf("update with %q: %v", value, err)
		}
		if !settings.Configured || settings.WebhookURL != "" {
			t.Fatalf("update with %q broke the stored webhook: %+v", value, settings)
		}
	}
	config, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if config.WebhookURL != riskTestRiskWebhook {
		t.Fatalf("stored webhook changed by a masked/blank update: %q", config.WebhookURL)
	}
	if len(config.MentionedList) != 2 || config.MentionedList[0] != "a" || config.MentionedList[1] != "b" {
		t.Fatalf("mentioned list must stay deduplicated: %v", config.MentionedList)
	}
}

// 非企业微信群机器人地址必须被拒绝，且错误是风控专用的那一个
// （两个输入框并排，报错必须指明是哪一个）。
func TestRiskWecomSettingsRejectsNonWecomURL(t *testing.T) {
	db := newRiskAlertTestDB(t)
	enabled := true
	for _, value := range []string{
		"http://example.com/hook",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://evil.example.com/cgi-bin/webhook/send?key=x",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x#frag",
	} {
		if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{Enabled: &enabled, WebhookURL: value}); !errors.Is(err, ErrInvalidRiskWecomWebhook) {
			t.Fatalf("value %q: err = %v, want ErrInvalidRiskWecomWebhook", value, err)
		}
	}
	if !strings.Contains(ErrInvalidRiskWecomWebhook.Error(), "风控") {
		t.Fatalf("error text must name the risk input box: %q", ErrInvalidRiskWecomWebhook.Error())
	}
}

// ============================================================
// 3 条欠款规则
// ============================================================

// 触发 1：单用户 |balance| >= 100 且 24h 内只告警一次（118 §2.4 / §2.7 例 5）。
func TestRiskEvaluateDebtAlertsUserRuleThresholdAndThrottle(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	below := entity.User{Username: "below", Balance: -99.99, Status: "enabled", Role: "user"}
	above := entity.User{Username: "above", Balance: -150, Status: "enabled", Role: "user"}
	edge := entity.User{Username: "edge", Balance: -100, Status: "enabled", Role: "user"}
	for _, user := range []*entity.User{&below, &above, &edge} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	result, err := EvaluateDebtAlerts(db, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 阈值是 >=：-100 必须命中，-99.99 必须不命中（边界最容易写错成 > ）。
	var warnings []entity.RiskWarning
	if err := db.Order("id ASC").Find(&warnings).Error; err != nil {
		t.Fatalf("load warnings: %v", err)
	}
	subjects := map[string]bool{}
	for _, warning := range warnings {
		if warning.RuleID == entity.RiskRuleIDDebtUser {
			subjects[warning.SubjectKey] = true
		}
	}
	if subjects["user:"+itoa(above.ID)] != true || subjects["user:"+itoa(edge.ID)] != true {
		t.Fatalf("|balance| >= 100 must alert (inclusive), got %v", subjects)
	}
	if subjects["user:"+itoa(below.ID)] {
		t.Fatalf("|balance| = 99.99 must not alert, got %v", subjects)
	}
	if result.Created != 2 || len(result.RuleIDs) != 1 || result.RuleIDs[0] != entity.RiskRuleIDDebtUser {
		t.Fatalf("unexpected evaluation result: %+v", result)
	}
	// 24h 内重复判定：不新增行（118 §2.7 例 5）。
	again, err := EvaluateDebtAlerts(db, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("evaluate again: %v", err)
	}
	if again.Created != 0 {
		t.Fatalf("within 24h the same user must not alert again, created=%d", again.Created)
	}
	// 24h 之后：重新告警（窗口是「上次告警 + 24h」，不是「自然日」）。
	later, err := EvaluateDebtAlerts(db, now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("evaluate later: %v", err)
	}
	if later.Created != 2 {
		t.Fatalf("after the throttle window the same users must alert again, created=%d", later.Created)
	}
}

// 阈值可配置且**不硬编码**：改系统设置即可用新阈值（96 §2.2 / 118 §2.5）。
func TestRiskEvaluateDebtAlertsUserRuleThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 阈值调到 10：一个 -50 的用户必须命中（默认 100 时不会）。
	putRiskSetting(t, db, "risk_rule_"+entity.RiskRuleIDDebtUser+"_threshold", "10")
	user := entity.User{Username: "small", Balance: -50, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, time.Now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", entity.RiskRuleIDDebtUser).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("configured threshold 10 must fire for -50, count=%d", count)
	}
	// 证据里的 threshold 必须是**配置后的** 10，不是代码默认的 100。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", entity.RiskRuleIDDebtUser).First(&warning).Error; err != nil {
		t.Fatalf("load warning: %v", err)
	}
	detail := map[string]any{}
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail must be valid JSON: %v (%s)", err, warning.Detail)
	}
	if value, ok := detail["threshold"].(float64); !ok || value != 10 {
		t.Fatalf("detail.threshold = %v, want 10 (must snapshot the configured value)", detail["threshold"])
	}
}

// 无效阈值必须**继续回落**，绝不退化成 0（0 会让每个用户都告警 = 刷屏）。
func TestRiskRuleThresholdNeverDegradesToZero(t *testing.T) {
	db := newRiskAlertTestDB(t)
	rule := entity.RiskRule{RuleID: entity.RiskRuleIDDebtUser, Threshold: 0}
	for _, bad := range []string{"abc", "-5", "0", "  ", "NaN-ish"} {
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtUser), bad)
		if got := riskRuleThreshold(db, rule, 100); got != 100 {
			t.Fatalf("bad setting %q produced threshold %v, want the code default 100", bad, got)
		}
	}
	// 设置合法 → 用设置；设置缺失但有规则列 → 用规则列。
	putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtUser), "250")
	if got := riskRuleThreshold(db, rule, 100); got != 250 {
		t.Fatalf("valid setting ignored: got %v, want 250", got)
	}
	putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtUser), "")
	rule.Threshold = 400
	if got := riskRuleThreshold(db, rule, 100); got != 400 {
		t.Fatalf("rule column ignored: got %v, want 400", got)
	}
}

// 触发 2：单批次欠款 >= 1000，且**每批次只告警一次**（不依赖时间窗）。
func TestRiskEvaluateDebtAlertsBatchRuleAlertsOncePerBatch(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	user := entity.User{Username: "roller", Balance: -1200, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 118 §5.2 的簿记形态：本金那一笔把余额从 0 扣到 -1200。
	record := entity.BillingRecord{
		UserID: user.ID, Type: riskBatchReversalBillingType,
		RequestID:     "admin_grant_reversal_principal_" + itoa(user.ID) + "_batch-7f3a",
		BalanceBefore: 0, BalanceAfter: -1200, Amount: 1200,
		CreatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create billing record: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var batchWarnings []entity.RiskWarning
	if err := db.Where("rule_id = ?", entity.RiskRuleIDDebtBatch).Find(&batchWarnings).Error; err != nil {
		t.Fatalf("load batch warnings: %v", err)
	}
	if len(batchWarnings) != 1 {
		t.Fatalf("expected exactly 1 batch warning, got %d", len(batchWarnings))
	}
	if batchWarnings[0].SubjectKey != "batch:batch-7f3a" {
		t.Fatalf("subject = %q, want batch:batch-7f3a", batchWarnings[0].SubjectKey)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(batchWarnings[0].Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["debtAmount"].(float64); got != 1200 {
		t.Fatalf("detail.debtAmount = %v, want 1200", detail["debtAmount"])
	}
	if got, _ := detail["userCount"].(float64); got != 1 {
		t.Fatalf("detail.userCount = %v, want 1", detail["userCount"])
	}
	// 「一次」不是「24h 一次」：throttle=0 表示永不重复（118 §2.4 触发 2）。
	for _, offset := range []time.Duration{time.Hour, 48 * time.Hour, 90 * 24 * time.Hour} {
		if _, err := EvaluateDebtAlerts(db, now.Add(offset)); err != nil {
			t.Fatalf("evaluate at +%v: %v", offset, err)
		}
	}
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", entity.RiskRuleIDDebtBatch).Count(new(int64)).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", entity.RiskRuleIDDebtBatch).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("batch rule must fire once for the whole batch regardless of elapsed time, count=%d", count)
	}
}

// 【队长复核项】三条规则的阈值必须**都可配置**，不允许把阈值写成常量和实际值比较。
//
// 本用例逐条验证「改 system_settings 就能改判定结果」，并对每条规则做**双向**断言：
// 阈值调低 → 原本不命中的场景要命中；阈值调高 → 原本命中的场景要不命中。
//
// 为什么必须双向：只测「调低后命中」的话，把阈值硬编码成一个很小的常量也能过；
// 只有同一条规则在两种配置下给出两种不同判定，才证明判定真的读了配置。
func TestRiskEvaluateDebtAlertsAllThresholdsAreConfigurable(t *testing.T) {
	t.Run("debt_user", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		user := entity.User{Username: "u", Balance: -300, Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		// 阈值调到 500（高于 300）→ 不命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtUser), "500")
		if _, err := EvaluateDebtAlerts(db, time.Now()); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtUser); c != 0 {
			t.Fatalf("threshold 500 must not fire for a -300 user, count=%d", c)
		}
		// 阈值调到 200（低于 300）→ 命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtUser), "200")
		if _, err := EvaluateDebtAlerts(db, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtUser); c != 1 {
			t.Fatalf("threshold 200 must fire for a -300 user, count=%d", c)
		}
	})

	t.Run("debt_batch", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := time.Now()
		user := entity.User{Username: "u", Balance: -1500, Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		record := entity.BillingRecord{
			UserID: user.ID, Type: riskBatchReversalBillingType,
			RequestID:     "admin_grant_reversal_principal_" + itoa(user.ID) + "_batch-cfg",
			BalanceBefore: 0, BalanceAfter: -1500, Amount: 1500,
			CreatedAt: now.Add(-time.Minute),
		}
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("create billing record: %v", err)
		}
		// 阈值 5000（高于 1500）→ 不命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtBatch), "5000")
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtBatch); c != 0 {
			t.Fatalf("threshold 5000 must not fire for 1500 debt, count=%d", c)
		}
		// 阈值 1000（默认值，低于 1500）→ 命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtBatch), "1000")
		if _, err := EvaluateDebtAlerts(db, now.Add(time.Hour)); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtBatch); c != 1 {
			t.Fatalf("threshold 1000 must fire for 1500 debt, count=%d", c)
		}
	})

	t.Run("debt_system", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		base := time.Now()
		// 第一轮记基线（总额 0）。
		if _, err := EvaluateDebtAlerts(db, base); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		// 新增欠款 2000。
		user := entity.User{Username: "u", Balance: -2000, Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		// 阈值 5000（高于 2000）→ 不命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtSystem), "5000")
		if _, err := EvaluateDebtAlerts(db, base.Add(time.Hour)); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtSystem); c != 0 {
			t.Fatalf("threshold 5000 must not fire for a 2000 increment, count=%d", c)
		}
		// 阈值 1000（低于 2000）→ 命中。
		putRiskSetting(t, db, riskRuleThresholdSettingKey(entity.RiskRuleIDDebtSystem), "1000")
		if _, err := EvaluateDebtAlerts(db, base.Add(2*time.Hour)); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if c := riskCountWarnings(t, db, entity.RiskRuleIDDebtSystem); c != 1 {
			t.Fatalf("threshold 1000 must fire for a 2000 increment, count=%d", c)
		}
	})
}

// riskCountWarnings 统计某规则的告警行数。
func riskCountWarnings(t *testing.T, db *gorm.DB, ruleID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", ruleID).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", ruleID, err)
	}
	return count
}

// 批次欠款按「账前/账后欠款之差」累计，不是直接看 amount：
// 本来有钱的用户回滚不产生欠款，本来就欠款的用户则全额计入。
func TestRiskDebtFormedOnlyCountsNewDebt(t *testing.T) {
	cases := []struct {
		name          string
		before, after float64
		want          float64
	}{
		{name: "had_money_still_solvent", before: 500, after: 100, want: 0},
		{name: "had_money_now_debt", before: 100, after: -40, want: 40},
		{name: "already_in_debt", before: -200, after: -350, want: 150},
		{name: "debt_fully_repaid_then_some", before: -100, after: 50, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := riskDebtFormed(tc.before, tc.after); got != tc.want {
				t.Fatalf("riskDebtFormed(%v, %v) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}

// 触发 3：系统总欠款 24h 增量 >= 5000；首次运行**只记基线不告警**。
func TestRiskEvaluateDebtAlertsSystemRuleUsesBaselineAndAlertsOnIncrement(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	base := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	// 起始总欠款 1000（低于阈值，也远低于 5000 增量）。
	users := []entity.User{
		{Username: "a", Balance: -600, Status: "enabled", Role: "user"},
		{Username: "b", Balance: -400, Status: "enabled", Role: "user"},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	// 第一轮：没有基线 → 记基线、**系统规则不告警**（fail-safe，见 riskSystemDebtBaselineAtKey 注释）。
	//
	// 注意：这两个用户各自 -600 / -400 都超过 debt_user 的 100 阈值，
	// 所以本轮**会**产生 2 条 debt_user 告警 —— 这里要断言的是「其中没有 debt_system」，
	// 而不是「一条都没有」（后者会把 debt_user 的判定也一起否掉，是两条不同的规则）。
	first, err := EvaluateDebtAlerts(db, base)
	if err != nil {
		t.Fatalf("first evaluate: %v", err)
	}
	for _, ruleID := range first.RuleIDs {
		if ruleID == entity.RiskRuleIDDebtSystem {
			t.Fatalf("first run must not fire the system rule, got rules=%v", first.RuleIDs)
		}
	}
	var systemCount int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", entity.RiskRuleIDDebtSystem).Count(&systemCount).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if systemCount != 0 {
		t.Fatalf("first run must not alert, got %d system warnings", systemCount)
	}
	if raw := getSystemSettingStringWithDefault(db, riskSystemDebtBaselineValueKey, ""); raw == "" {
		t.Fatal("baseline value must be recorded on the first run")
	}
	// 欠款暴涨：再加 6000 → 当前 7000，增量 6000 >= 5000 → 命中。
	huge := entity.User{Username: "c", Balance: -6000, Status: "enabled", Role: "user"}
	if err := db.Create(&huge).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	second, err := EvaluateDebtAlerts(db, base.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("second evaluate: %v", err)
	}
	if second.Created == 0 {
		t.Fatal("increment over the threshold must create a system warning")
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", entity.RiskRuleIDDebtSystem).First(&warning).Error; err != nil {
		t.Fatalf("load system warning: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["increment"].(float64); got != 6000 {
		t.Fatalf("detail.increment = %v, want 6000", detail["increment"])
	}
	if got, _ := detail["baselineDebt"].(float64); got != 1000 {
		t.Fatalf("detail.baselineDebt = %v, want 1000", detail["baselineDebt"])
	}
	// 6h 节流：同一主体在 6h 内不重复告警。
	third, err := EvaluateDebtAlerts(db, base.Add(7*time.Hour))
	if err != nil {
		t.Fatalf("third evaluate: %v", err)
	}
	if third.Created != 0 {
		t.Fatalf("system rule must be throttled for 6h, created=%d", third.Created)
	}
}

// 96 C1：只告警不拦截 —— 判定过程**不得**改动任何用户状态或余额。
//
// 这条用例是 C1 的可执行形式：跑两轮判定后逐字段比对 users 行，
// 任何「顺手封禁/顺手清零」的实现都会在这里变红。
func TestRiskEvaluateDebtAlertsNeverMutatesUsers(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	user := entity.User{Username: "victim", Balance: -5000, GiftBalance: 12, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	var before entity.User
	if err := db.First(&before, user.ID).Error; err != nil {
		t.Fatalf("load before: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := EvaluateDebtAlerts(db, now.Add(time.Duration(i)*7*time.Hour)); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("load after: %v", err)
	}
	if after.Balance != before.Balance || after.GiftBalance != before.GiftBalance {
		t.Fatalf("wind control must not touch balances: before=%+v after=%+v", before, after)
	}
	if after.Status != before.Status || after.Status != "enabled" {
		t.Fatalf("wind control must not change user status (96 C1): %q -> %q", before.Status, after.Status)
	}
}

// Detail 必须是**证据快照 JSON**（96 §2.3），且包含触发时刻的关键字段。
func TestRiskWarningDetailIsJSONEvidenceSnapshot(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	user := entity.User{Username: "snapshot", Balance: -150, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ? AND subject_key = ?", entity.RiskRuleIDDebtUser, "user:"+itoa(user.ID)).First(&warning).Error; err != nil {
		t.Fatalf("load warning: %v", err)
	}
	if warning.UserID == nil || *warning.UserID != user.ID {
		t.Fatalf("user_id not recorded: %+v", warning.UserID)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail must be valid JSON: %v (%s)", err, warning.Detail)
	}
	for _, key := range []string{"ruleId", "subjectKey", "threshold", "value", "balance", "evaluatedAt"} {
		if _, ok := detail[key]; !ok {
			t.Fatalf("detail missing %q: %s", key, warning.Detail)
		}
	}
	if got, _ := detail["value"].(float64); got != 150 {
		t.Fatalf("detail.value = %v, want 150 (the debt magnitude at trigger time)", detail["value"])
	}
	if got, _ := detail["evaluatedAt"].(string); got != now.Format(time.RFC3339) {
		t.Fatalf("detail.evaluatedAt = %q, want %q (frozen snapshot, not a live query)", got, now.Format(time.RFC3339))
	}
}

// 完全未配置风控 webhook 时，判定流程必须**照常落库**，一条都不推送。
//
// 这是 C2 在端到端层的形态：端到端跑一遍，sender 一次都不该被调用。
func TestRiskEvaluateDebtAlertsRecordsWithoutAnyRiskWebhook(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, TicketWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, TicketWecomWebhookURLSettingKey, riskTestTicketWebhook)
	recorder := &riskSenderRecorder{}
	recorder.install(t)
	user := entity.User{Username: "debtor", Balance: -500, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	result, err := EvaluateDebtAlerts(db, time.Now())
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Created == 0 {
		t.Fatal("warnings must still be recorded without a webhook")
	}
	if result.Pushed != 0 {
		t.Fatalf("nothing must be pushed without a risk webhook, pushed=%d", result.Pushed)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("sender must never be called without a risk webhook (96 C2), got %d calls", len(calls))
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Count(&count).Error; err != nil || count != int64(result.Created) {
		t.Fatalf("warning rows must equal created count: rows=%d created=%d err=%v", count, result.Created, err)
	}
}

// 禁用的规则不参与判定（运营要能一条条关掉）。
func TestRiskEvaluateDebtAlertsRespectsDisabledRules(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", entity.RiskRuleIDDebtUser).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	user := entity.User{Username: "debtor", Balance: -9999, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, time.Now()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", entity.RiskRuleIDDebtUser).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("disabled rule must not fire, count=%d", count)
	}
}

// 规则行被删光时用代码默认值兜底：少一条规则等于少一道安全阀（118 §2.9）。
//
// ⚠️ 这里的阈值断言必须写**字面量 100 / 1000 / 5000**，不能写成
// `rule.Threshold != riskDefaultThreshold(rule.RuleID)` —— 那是拿函数输出和它自己比，
// 默认值从 100 改成 10000 时等式两边一起变，**永远绿**（自我指涉断言）。
// 实测过：旧写法下把 100 变异成 10000，本条用例仍然通过。
func TestLoadRiskRulesFallsBackToCodeDefaults(t *testing.T) {
	db := newRiskAlertTestDB(t)
	rules, err := loadRiskRules(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 fallback rules, got %d", len(rules))
	}
	// 配置完全缺失时的默认阈值（118 §2.5 用户拍板的 100 / 1000 / 5000）。
	wantThreshold := map[string]float64{
		entity.RiskRuleIDDebtUser:   100,
		entity.RiskRuleIDDebtBatch:  1000,
		entity.RiskRuleIDDebtSystem: 5000,
	}
	for _, rule := range rules {
		if !rule.Enabled {
			t.Fatalf("fallback rule %s must be enabled", rule.RuleID)
		}
		want, ok := wantThreshold[rule.RuleID]
		if !ok {
			t.Fatalf("unexpected fallback rule id %q", rule.RuleID)
		}
		if rule.Threshold != want {
			t.Fatalf("fallback rule %s threshold = %v, want %v（默认值被改动会漏报）", rule.RuleID, rule.Threshold, want)
		}
	}
	// 对照：riskDefaultThreshold 也必须与同一批字面量一致
	// （它被 riskRuleThreshold 的第三层兜底使用，两处默认值不能漂移）。
	for ruleID, want := range wantThreshold {
		if got := riskDefaultThreshold(ruleID); got != want {
			t.Fatalf("riskDefaultThreshold(%s) = %v, want %v", ruleID, got, want)
		}
	}
}

// 【默认值安全边界】配置缺失时，风控告警必须**默认不推送**（fail-closed）。
//
// 96 §4.1 的三元组一个都没配时：RiskAlertConfig 必须返回 Enabled=false。
// 这条是「默认值即安全边界」的行为断言 —— 没有它，把解析改成
// `!strings.EqualFold(value, "false")`（缺 key 即视为启用）也全绿，
// 那会让「什么都没配」的系统朝着某个残留 URL 推送（实测该变异确实全绿）。
func TestRiskAlertConfigDefaultsToDisabledWhenUnset(t *testing.T) {
	db := newRiskAlertTestDB(t)
	config, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if config.Enabled {
		t.Fatal("风控通知在未配置时必须默认关闭（fail-closed），否则缺 key 即视为启用")
	}
	if config.WebhookURL != "" || len(config.MentionedList) != 0 {
		t.Fatalf("未配置时其余字段也必须为空: %+v", config)
	}
	// 三态显式区分：只有字面 true / 1 才算启用，"yes"/"TRUE " 之外不得放开。
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"no", false},
		{"yes", false}, // 不接受 yes
		{"true", true},
		{"1", true},
		{"TRUE", true},     // 大小写不敏感
		{"  true  ", true}, // 两侧空白容忍
	} {
		putRiskSetting(t, db, RiskWecomEnabledSettingKey, tc.raw)
		got, err := RiskAlertConfig(db)
		if err != nil {
			t.Fatalf("config for %q: %v", tc.raw, err)
		}
		if got.Enabled != tc.want {
			t.Fatalf("enabled for %q = %v, want %v", tc.raw, got.Enabled, tc.want)
		}
	}
}

// request_id 解析失败必须跳过而不是猜（118 §5.2 的三段式是幂等键契约）。
func TestRiskBatchIDFromRequestID(t *testing.T) {
	cases := []struct {
		requestID string
		want      string
	}{
		{"admin_grant_reversal_principal_12345_batch-7f3a", "batch-7f3a"},
		{"admin_grant_reversal_principal_1_b", "b"},
		{"admin_grant_reversal_gift_12345_batch-7f3a", ""},
		{"admin_grant_reversal_principal_abc_batch-1", ""},
		{"admin_grant_reversal_principal_12345_", ""},
		{"admin_grant_reversal_principal_", ""},
		{"", ""},
		{"random", ""},
	}
	for _, tc := range cases {
		if got := riskBatchIDFromRequestID(tc.requestID); got != tc.want {
			t.Fatalf("riskBatchIDFromRequestID(%q) = %q, want %q", tc.requestID, got, tc.want)
		}
	}
}

// 只读列表：筛选、分页、以及「非法分页参数回落默认」。
func TestRiskWarningListFiltersAndPagination(t *testing.T) {
	db := newRiskAlertTestDB(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		row := entity.RiskWarning{
			RuleID:     entity.RiskRuleIDDebtUser,
			SubjectKey: "user:" + itoa(int32(i+1)),
			Severity:   entity.RiskSeverityMedium,
			Notified:   i%2 == 0,
			CreatedAt:  now.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	other := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtSystem, SubjectKey: "system:debt", Severity: entity.RiskSeverityHigh, CreatedAt: now}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	notified := true
	result, err := RiskWarningList(db, RiskWarningFilter{RuleID: entity.RiskRuleIDDebtUser, Notified: &notified, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 3 || len(result.Data) != 3 {
		t.Fatalf("filtered total = %d rows=%d, want 3", result.Total, len(result.Data))
	}
	for _, row := range result.Data {
		if !row.Notified {
			t.Fatalf("notified filter leaked a row: %+v", row)
		}
	}
	// 非法分页参数回落默认，而不是 400 或空页。
	fallback, err := RiskWarningList(db, RiskWarningFilter{Page: -3, PageSize: 9999})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if fallback.Page != 1 || fallback.PageSize != riskWarningListPageSizeDefault {
		t.Fatalf("bad pagination not clamped: page=%d pageSize=%d", fallback.Page, fallback.PageSize)
	}
	if fallback.Total != 6 {
		t.Fatalf("total = %d, want 6", fallback.Total)
	}
	// 按 id DESC 排序：最新一条在最前。
	if len(fallback.Data) == 0 || fallback.Data[0].ID <= fallback.Data[len(fallback.Data)-1].ID {
		t.Fatalf("list must be ordered by id DESC: %+v", fallback.Data)
	}
}

// 详情读取。
func TestRiskWarningDetailLoadsRow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	row := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtBatch, SubjectKey: "batch:x", Severity: entity.RiskSeverityHigh, Detail: `{"debtAmount":1200}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	detail, err := RiskWarningDetail(db, row.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.ID != row.ID || detail.SubjectKey != "batch:x" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	if _, err := RiskWarningDetail(db, 0); err == nil {
		t.Fatal("id <= 0 must be rejected")
	}
	if _, err := RiskWarningDetail(db, 99999); err == nil {
		t.Fatal("missing row must be an error")
	}
}

// 发信失败必须**只记日志**：告警行已经写好，推送失败不能回滚它，也不能中断整轮。
func TestRiskEvaluateDebtAlertsSendFailureKeepsWarningAndContinues(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, RiskWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, RiskWecomWebhookURLSettingKey, riskTestRiskWebhook)
	recorder := &riskSenderRecorder{onCall: func(riskSenderCall) error { return errors.New("simulated webhook outage") }}
	recorder.install(t)
	for i := 0; i < 2; i++ {
		user := entity.User{Username: "u" + itoa(int32(i)), Balance: -800, Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	result, err := EvaluateDebtAlerts(db, time.Now())
	if err != nil {
		t.Fatalf("a push failure must not fail the whole evaluation: %v", err)
	}
	if result.Created != 2 || result.Pushed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("warning rows must survive a push failure, count=%d err=%v", count, err)
	}
	var unnotified int64
	if err := db.Model(&entity.RiskWarning{}).Where("notified = ?", false).Count(&unnotified).Error; err != nil || unnotified != 2 {
		t.Fatalf("failed pushes must stay notified=false, count=%d err=%v", unnotified, err)
	}
	if calls := recorder.snapshot(); len(calls) != 2 {
		t.Fatalf("both warnings must be attempted, got %d calls", len(calls))
	}
}

// 测试发信走的是**风控**配置；风控未配置时必须直接拒绝，绝不借工单通道（96 C2）。
func TestSendRiskTestWebhookRequiresRiskConfig(t *testing.T) {
	if err := SendRiskTestWebhook(context.Background(), RiskWecomConfig{WebhookURL: ""}, "x", nil); !errors.Is(err, ErrInvalidRiskWecomWebhook) {
		t.Fatalf("empty risk webhook must be rejected, got %v", err)
	}
	if err := SendRiskTestWebhook(context.Background(), RiskWecomConfig{WebhookURL: "http://evil.example/hook"}, "x", nil); !errors.Is(err, ErrInvalidRiskWecomWebhook) {
		t.Fatalf("non-wecom risk webhook must be rejected, got %v", err)
	}
}

// riskWarningExists 的窗口语义：`> ?` 是排他边界，避免「同一时刻」被算成窗口内。
func TestRiskWarningExistsRespectsWindowBoundary(t *testing.T) {
	db := newRiskAlertTestDB(t)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	row := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtUser, SubjectKey: "user:1", Severity: entity.RiskSeverityMedium, CreatedAt: now.Add(-24 * time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// 恰好 24h 之前的行**不在**窗口内（边界排他）→ 应当再次告警。
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
		Where("rule_id = ? AND subject_key = ?", entity.RiskRuleIDDebtUser, "user:1").
		Where("created_at > ?", now.Add(-24*time.Hour))
	found, err := riskWarningExists(query, nil)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if found {
		t.Fatal("a row exactly at the window boundary must not suppress a new alert")
	}
	// 窗口内 1 秒 → 应当被抑制。
	inside := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskWarning{}).
		Where("rule_id = ? AND subject_key = ?", entity.RiskRuleIDDebtUser, "user:1").
		Where("created_at > ?", now.Add(-24*time.Hour-time.Second))
	if found, err := riskWarningExists(inside, nil); err != nil || !found {
		t.Fatalf("row inside the window must suppress, found=%v err=%v", found, err)
	}
}

// riskRuleDisplayName / 消息渲染的兜底：Detail 坏掉时告警仍要能发出去。
func TestBuildRiskWarningMarkdownToleratesBrokenDetail(t *testing.T) {
	warning := entity.RiskWarning{
		ID: 42, RuleID: entity.RiskRuleIDDebtUser, Severity: entity.RiskSeverityMedium,
		SubjectKey: "user:1", Detail: "{not json", CreatedAt: time.Now(),
	}
	content, err := buildRiskWarningMarkdown(warning)
	if err != nil {
		t.Fatalf("broken detail must not block the message: %v", err)
	}
	if !strings.Contains(content, "风控告警") || !strings.Contains(content, riskAlertDetailPathPrefix+"42") {
		t.Fatalf("unexpected content: %s", content)
	}
	if riskRuleDisplayName("") != "未知规则" || riskRuleDisplayName("debt_user") == "debt_user" {
		t.Fatalf("display name fallback wrong: %q / %q", riskRuleDisplayName(""), riskRuleDisplayName("debt_user"))
	}
}

// 注：`itoa(int32) string` 已由同包的 registration_clawback_test.go:112 提供，
// 这里不再重复定义（重复声明会编译失败）。int64 场景用 strconv.FormatInt 直接转。
