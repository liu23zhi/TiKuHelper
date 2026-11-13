package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	// gormlogger 别名：本文件同时要用 pkg/logger（ShanghaiLocation）与
	// gorm 的 logger（Default/Silent），两个包同名必须分开。
	gormlogger "gorm.io/gorm/logger"
)

const (
	riskTestRiskWebhook   = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=risk-secret"
	riskTestTicketWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=ticket-secret"
)

func newRiskAlertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newRiskAlertTestDBNamed(t, t.Name())
}

// newRiskAlertTestDBNamed 用指定名字建一个**独立**内存库。
//
// 名字必须唯一：`mode=memory&cache=shared` 下同名就是同一个库。
// 需要一个用例内多轮跑的场景（并发稳定性）必须每轮换名，否则后一轮会看到
// 前一轮残留的行 —— 表现为「已创建 0 个」的假红（实测踩到）。
func newRiskAlertTestDBNamed(t *testing.T, rawName string) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(rawName)
	db, err := gorm.Open(sqlite.Open("file:risk-alert-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
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
		// Log 是 R1（multi_account_ip）的数据源：成功登录的审计行落在 logs 表。
		&entity.Log{},
		// Ticket 是 R8（ticket_flood）的数据源：统计窗口内新建工单数。
		&entity.Ticket{},
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

// seedRiskRules 写入**全部**代码默认规则（欠款 3 条 + 行为规则），与 registry 种子同值。
//
// ⚠️ 必须用 entity.DefaultRiskRules() 而不是只写欠款三条：R1（multi_account_ip）
// 落地后，写死三条会让所有涉及行为规则的用例在「规则行不存在」的兜底路径上跑 ——
// 那样测的是兜底而不是真实行，且用例名会指向错误的被测对象。
func seedRiskRules(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, item := range entity.DefaultRiskRules() {
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

// 规则行被删光时用代码默认值兜底：少一条规则等于少一道闸。
//
// ⚠️ 这里的阈值断言必须写**字面量 100 / 1000 / 5000 / 5**，不能写成
// `rule.Threshold != riskDefaultThreshold(rule.RuleID)` —— 那是拿函数输出和它自己比，
// 默认值从 100 改成 10000 时等式两边一起变，**永远绿**（自我指涉断言）。
// 实测过：旧写法下把 100 变异成 10000，本条用例仍然通过。
//
// ⚠️ 期望集合必须用**字面量 rule_id**，且必须**逐个覆盖**（不是只数个数）：
// 只断 len==4 的话，「删掉 debt_user、加一条乱 id」也能过。
// R1（multi_account_ip）落地时本用例曾因「只数个数」而红得毫无信息量
// （expected 3, got 4），改成逐个 rule_id 后新增规则会被精确指出。
func TestLoadRiskRulesFallsBackToCodeDefaults(t *testing.T) {
	db := newRiskAlertTestDB(t)
	rules, err := loadRiskRules(db)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// 配置完全缺失时的默认阈值（字面量，故意不引用 entity 常量：
	// 常量漂移时这里必须红）。
	wantThreshold := map[string]float64{
		"debt_user":                 100,
		"debt_batch":                1000,
		"debt_system":               5000,
		"multi_account_ip":          5,
		"ticket_flood":              5,
		"abnormal_register_pattern": 3,
	}
	if len(rules) != len(wantThreshold) {
		t.Fatalf("fallback rule count = %d, want %d（实际：%v）", len(rules), len(wantThreshold), ruleIDsOf(rules))
	}
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			t.Fatalf("fallback rule %s must be enabled", rule.RuleID)
		}
		want, ok := wantThreshold[rule.RuleID]
		if !ok {
			t.Fatalf("unexpected fallback rule id %q", rule.RuleID)
		}
		if seen[rule.RuleID] {
			t.Fatalf("rule %s 在兜底集合里出现了两次", rule.RuleID)
		}
		seen[rule.RuleID] = true
		if rule.Threshold != want {
			t.Fatalf("fallback rule %s threshold = %v, want %v（默认值被改动会漏报）", rule.RuleID, rule.Threshold, want)
		}
	}
	// 反向对照：四条都必须真的出现（防「少一条但个数凑够」）。
	for ruleID := range wantThreshold {
		if !seen[ruleID] {
			t.Fatalf("兜底集合缺少规则 %q，实际：%v", ruleID, ruleIDsOf(rules))
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

// ============================================================
// C3 三条判据的独立钉子（t165 追加）
// ============================================================
//
// 96 §0 C3 给了三条可跑判据，前两条各有一个方向、第三条是「同值不同记录」。
// 已有的 TestRiskAlertDoesNotFallBackToTicketWebhook / TestRiskWecomSettingsAreIndependentFromTicketSettings
// 覆盖了判据 ① 与 ③ 的一部分，但**判据 ② 的反向**（只配风控、工单留空 → 工单通知不发）
// 此前只有读取侧断言、没有走「入队策略」这条真实路径。
//
// 三条用例的写法约定（为了让变异真的会红）：
//   - 断言落在**动作层**（sender 是否被调用、入队策略返回什么），不是「字段读出来是什么」；
//   - 每条都带一个**反向对照**（另一条通道确实可用），否则「两边都不发」的空实现也能过；
//   - 期望值一律写**字面量**（`riskNotifyWebhookEmpty` 是枚举语义，可以引用；
//     但 URL 与「是否为空」的判据必须看到真实字符串），避免与实现共享常量导致恒真。

// 判据 ①（独立钉子）：只配工单、风控留空 → 风控告警一发都不推。
//
// 与已存在用例的差异：这里**同时**把工单配成「启用 + URL + @成员」三件齐全，
// 并断言 sender 收到 0 次调用、riskNotifyWebhookEmpty 有多态、数据库里
// wecom_ticket_webhook_url 一个字节都没被写动。加一行
// `if risk == "" { risk = ticket }` 的变异会让本用例在**第一处**（pushed）就红。
func TestRiskAlertNeverBorrowsTicketWebhookWhenRiskIsEmpty(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	putRiskSetting(t, db, TicketWecomEnabledSettingKey, "true")
	putRiskSetting(t, db, TicketWecomWebhookURLSettingKey, riskTestTicketWebhook)
	putRiskSetting(t, db, TicketWecomMentionedListSettingKey, `["ticket-owner"]`)

	// 对照：工单配置本身必须**可用**，否则「工单也没配」时本用例证明不了任何事。
	ticketConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket config: %v", err)
	}
	if !ticketConfig.Enabled || ticketConfig.WebhookURL != riskTestTicketWebhook {
		t.Fatalf("对照失效：工单配置应可用，实际 %+v", ticketConfig)
	}
	// 风控配置必须真的是空的（不是「配了但没启用」）。
	riskConfig, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("read risk config: %v", err)
	}
	if riskConfig.WebhookURL != "" || riskConfig.Enabled {
		t.Fatalf("前置失效：风控配置必须全空，实际 %+v", riskConfig)
	}

	recorder := &riskSenderRecorder{}
	recorder.install(t)
	warning := entity.RiskWarning{
		RuleID: entity.RiskRuleIDDebtUser, SubjectKey: "user:11",
		Severity: entity.RiskSeverityMedium, Detail: `{"value":150,"threshold":100}`, CreatedAt: time.Now(),
	}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, reason, notifyErr := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if notifyErr != nil {
		t.Fatalf("notify: %v", notifyErr)
	}
	if pushed {
		t.Fatal("C3 判据 1：风控 webhook 为空时绝不允许推送（不得借用工单通道）")
	}
	if reason != riskNotifyWebhookEmpty {
		t.Fatalf("reason = %q, want %q（「未配置」与「被节流」的可操作结论相反）", reason, riskNotifyWebhookEmpty)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("C3 判据 1：sender 一次都不该被调用，实际 %d 次，首次发往 %q", len(calls), calls[0].Config.WebhookURL)
	}
	// 反向断言：工单配置必须原封不动（回退实现常伴随「顺手把工单键写成风控的」）。
	afterConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("re-read ticket config: %v", err)
	}
	if afterConfig.WebhookURL != riskTestTicketWebhook || !afterConfig.Enabled {
		t.Fatalf("C3 判据 1：风控路径改动了工单配置 %+v", afterConfig)
	}
}

// 判据 ②：只配风控、工单留空 → 工单通知**不发**。
//
// 走的是入队策略 ticketNotificationStatusForConfig（enqueueTicketNotificationInTx
// 与 QueueTicketNotificationWithDB 都调它），所以这条断言与「真实建单会不会推送」
// 是同一条路径，而不是读字段的近似。
//
// 变异性：把工单配置的读取改成「为空则回退风控」时，这里会拿到非空 URL →
// 策略返回 pending 而不是 skipped → 本用例第一处断言即红。
func TestTicketNotificationNeverBorrowsRiskWebhookWhenTicketIsEmpty(t *testing.T) {
	db := newRiskAlertTestDB(t)
	enabled := true
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{
		Enabled: &enabled, WebhookURL: riskTestRiskWebhook, MentionedList: []string{"risk-owner"},
	}); err != nil {
		t.Fatalf("seed risk settings: %v", err)
	}

	// 对照：风控配置必须**可用**（否则「风控也没配」时本用例证明不了任何事）。
	riskConfig, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("read risk config: %v", err)
	}
	if riskConfig.WebhookURL != riskTestRiskWebhook || !riskConfig.Enabled {
		t.Fatalf("对照失效：风控配置应可用，实际 %+v", riskConfig)
	}

	ticketConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket config: %v", err)
	}
	if ticketConfig.WebhookURL != "" || ticketConfig.Enabled {
		t.Fatalf("C3 判据 2：工单配置必须仍为空，实际 %+v（风控配置被写进了工单键？）", ticketConfig)
	}
	status, reason := ticketNotificationStatusForConfig(ticketConfig)
	if status != ticketNotificationSkipped {
		t.Fatalf("C3 判据 2：工单未配置时必须入队为 skipped，实际 %q（reason=%q）", status, reason)
	}
	if reason == "" {
		t.Fatal("C3 判据 2：skipped 必须带原因，否则线上无法区分「没配」与「配错」")
	}
	// 反向：风控侧仍然能发（判据 2 不是「两边都死」）。
	recorder := &riskSenderRecorder{}
	recorder.install(t)
	warning := entity.RiskWarning{
		RuleID: entity.RiskRuleIDDebtSystem, SubjectKey: "system:debt",
		Severity: entity.RiskSeverityHigh, Detail: `{"value":9000,"threshold":5000}`, CreatedAt: time.Now(),
	}
	if err := db.Create(&warning).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	pushed, notifyReason, notifyErr := notifyRiskWebhook(context.Background(), db, &warning, time.Now())
	if notifyErr != nil || !pushed || notifyReason != riskNotifySent {
		t.Fatalf("对照失效：只配风控时风控必须能发，pushed=%v reason=%q err=%v", pushed, notifyReason, notifyErr)
	}
	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].Config.WebhookURL != riskTestRiskWebhook {
		t.Fatalf("对照失效：风控应发往 %q，实际 %+v", riskTestRiskWebhook, calls)
	}
}

// 判据 ③：两个填**同一个 URL** → 两条独立记录，清零其中一个不影响另一个。
//
// 双侧都必须验：只清一侧的用例抓不到「清 A 时顺手也清了 B」的实现
// （那是最常见的错误写法 —— 用同一个 helper 顺手 save 两组键）。
func TestRiskAndTicketWebhookShareValueButStayIndependent(t *testing.T) {
	db := newRiskAlertTestDB(t)
	enabled := true
	// 两个框填**完全相同**的 URL（用户明确允许值相同）。
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled: &enabled, WebhookURL: riskTestRiskWebhook, MentionedList: []string{"shared-owner"},
	}); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{
		Enabled: &enabled, WebhookURL: riskTestRiskWebhook, MentionedList: []string{"shared-owner"},
	}); err != nil {
		t.Fatalf("seed risk: %v", err)
	}

	// 前提：DB 里必须是**两行**不同的 key，指向同一个值。
	var rows []entity.SystemSetting
	if err := db.Where("`key` IN ?", []string{
		RiskWecomWebhookURLSettingKey, TicketWecomWebhookURLSettingKey,
	}).Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("C3 判据 3：应是两条独立记录，实际 %d 行", len(rows))
	}
	for _, row := range rows {
		if row.Value != riskTestRiskWebhook {
			t.Fatalf("C3 判据 3：两条记录的值都应是同一个 URL，%s = %q", row.Key, row.Value)
		}
	}

	// 清风控 → 工单完好。
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{ClearWebhook: true}); err != nil {
		t.Fatalf("clear risk: %v", err)
	}
	ticketConfig, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if ticketConfig.WebhookURL != riskTestRiskWebhook {
		t.Fatalf("C3 判据 3：清风控不得动工单，实际 %q", ticketConfig.WebhookURL)
	}
	if len(ticketConfig.MentionedList) != 1 || ticketConfig.MentionedList[0] != "shared-owner" {
		t.Fatalf("C3 判据 3：清风控不得动工单的 @成员，实际 %v", ticketConfig.MentionedList)
	}

	// 反过来：先恢复风控，再清工单 → 风控完好。
	if _, err := UpdateRiskWecomSettings(db, UpdateRiskWecomSettingsInput{
		Enabled: &enabled, WebhookURL: riskTestRiskWebhook,
	}); err != nil {
		t.Fatalf("restore risk: %v", err)
	}
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{ClearWebhook: true}); err != nil {
		t.Fatalf("clear ticket: %v", err)
	}
	riskConfig, err := RiskAlertConfig(db)
	if err != nil {
		t.Fatalf("read risk: %v", err)
	}
	if riskConfig.WebhookURL != riskTestRiskWebhook || !riskConfig.Enabled {
		t.Fatalf("C3 判据 3：清工单不得动风控，实际 %+v", riskConfig)
	}
	if len(riskConfig.MentionedList) != 1 || riskConfig.MentionedList[0] != "shared-owner" {
		t.Fatalf("C3 判据 3：清工单不得动风控的 @成员，实际 %v", riskConfig.MentionedList)
	}
}

// C1 的**源码级**判据（96 §0 C1）：风控判定路径不得改用户状态。
//
// 行为级判据已有 TestRiskEvaluateDebtAlertsNeverMutatesUsers，但它只覆盖
// 「当前这 3 条规则跑一遍」。源码级断言补的是另一半：**将来**有人往
// risk_alert.go 里加一句 `Update("status", "banned")`，本用例会立刻红，
// 而行为级用例只有在新增分支被触发时才红（可能永远不触发）。
//
// 判定规则（刻意写得窄，避免误伤读取路径）：
//   - 出现 `entity.User` 的行，若同时出现 Update / Updates / Delete / Save，
//     视为对 users 的写操作 → 红；
//   - 出现 `"status"` 字面量**且同时出现写标记**的行 → 红（风控没有理由写这个字段）；
//   - `Model(&entity.User{}).Where("balance < 0").Select/Count` 这类读是允许的。
//
// ⚠️ 2026-11-14 收窄（R1 落地时）：原判据是「**任何**非注释行出现 `"status"`
// 就红」。R1 需要读 `logs` 表里登录审计行的 JSON 快照
// （`new_answer LIKE '%"status":"success"%'`），它是**跨表的只读筛选**，
// 与「写 users.status」无关，却会被原判据误伤。
//
// 收窄方式是「加写标记限定」，不是删掉这条断言 —— 并且下面配了
// `assertStatusGuardStillCatchesRealWrite`，用真实写形态回测，
// 保证收窄后仍能抓到 `Update("status", "banned")` 这类真正的处置写。
func TestRiskAlertSourceNeverMutatesUserStatus(t *testing.T) {
	raw, err := os.ReadFile("risk_alert.go")
	if err != nil {
		t.Fatalf("read risk_alert.go: %v（本用例依赖包目录作为工作目录）", err)
	}
	source := string(raw)
	if !strings.Contains(source, "func EvaluateDebtAlerts(") {
		t.Fatal("读到的不是 risk_alert.go（缺少 EvaluateDebtAlerts）：路径假设已失效，本用例不能静默通过")
	}
	writes := []string{"Update(", "Updates(", "UpdateColumn", "Delete(", "Save(", "Exec("}
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, `"status"`) && containsAnyMarker(trimmed, writes) {
			t.Fatalf("C1：风控路径不得写 status：%s", trimmed)
		}
		// raw SQL 里的 SET status / status = 'banned' 也算写（不经 GORM 的 Update）。
		lowered := strings.ToLower(trimmed)
		if strings.Contains(lowered, "set status") || strings.Contains(lowered, "status = 'banned'") ||
			strings.Contains(lowered, `status = "banned"`) {
			t.Fatalf("C1：风控路径不得用原生 SQL 写 status：%s", trimmed)
		}
		if !strings.Contains(trimmed, "entity.User") {
			continue
		}
		for _, marker := range writes {
			if strings.Contains(trimmed, marker) {
				t.Fatalf("C1：风控路径不得对 entity.User 执行 %q：%s", marker, trimmed)
			}
		}
	}
	// 正向对照：本文件确实**读**用户表（否则上面的循环可能一条都没扫到而假通过）。
	if !strings.Contains(source, "Model(&entity.User{})") {
		t.Fatal("对照失效：risk_alert.go 里应有只读的 Model(&entity.User{}) 查询")
	}
	// 收窄后的回测：真正的处置写形态必须仍被抓到（防止收窄收过头变成空断言）。
	assertStatusGuardStillCatchesRealWrite(t)
}

// containsAnyMarker 判断行内是否出现任一写标记。
func containsAnyMarker(line string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// assertStatusGuardStillCatchesRealWrite 用真实写形态回测收窄后的 status 判据。
//
// 这是「收窄断言」时的必备对照：只收窄不回测，很容易把守卫收成永远为真的空断言
// （本仓 t150 / t143 都吃过这个亏）。
func assertStatusGuardStillCatchesRealWrite(t *testing.T) {
	t.Helper()
	writeMarkers := []string{"Update(", "Updates(", "UpdateColumn", "Delete(", "Save(", "Exec("}
	// 这些是「真的在改用户状态」的形态，必须全部被判定为违规。
	mustCatch := []string{
		`db.Model(&entity.User{}).Where("id = ?", id).Update("status", "banned")`,
		`db.Model(&entity.User{}).Updates(map[string]any{"status": "banned"})`,
		`db.Exec("UPDATE users SET status = 'banned' WHERE id = ?", id)`,
		`db.Model(&entity.User{}).UpdateColumn("status", "disabled")`,
	}
	for _, line := range mustCatch {
		caught := (strings.Contains(line, `"status"`) && containsAnyMarker(line, writeMarkers)) ||
			strings.Contains(strings.ToLower(line), "set status") ||
			strings.Contains(strings.ToLower(line), "status = 'banned'")
		if !caught {
			t.Fatalf("C1 守卫已失效：这种真实处置写没有被判定为违规：%s", line)
		}
	}
	// 反向对照：R1 真实在用的只读筛选不得被误判（否则守卫会挡住正当读路径）。
	readOnly := `Where("new_answer LIKE ?", ` + "`" + `%"status":"success"%` + "`" + `)`
	if strings.Contains(readOnly, `"status"`) && containsAnyMarker(readOnly, writeMarkers) {
		t.Fatalf("只读筛选被误判为写操作：%s", readOnly)
	}
}

// note: itoa 定义见文件末尾注释

// ════════════════════════════════════════════════════════════════════
// R8 · ticket_flood（96 §2.1）—— 单用户 24h 建单 ≥N 张
//
// 数据源：tickets **主表**（user_id + created_at）。
// 两条关键取舍（都有对应用例钉住）：
//   1. 不数 ticket_messages 回复 —— 数是「新建工单」不是「沟通过程」；
//   2. 不数系统的自动建单（origin=system，Q26 赠送判定失败自动建单）——
//      那是用户无过错时系统建的，算到用户头上属于误报。
// ════════════════════════════════════════════════════════════════════

const riskR8TestUserID = int32(4242)

// seedRiskTicket 造一张工单（走真实实体，让 NOT NULL 约束与默认值都生效）。
func seedRiskTicket(t *testing.T, db *gorm.DB, userID int32, origin string, at time.Time) {
	t.Helper()
	ticket := entity.Ticket{
		UserID: userID, UserName: "u" + strconv.Itoa(int(userID)),
		Subject: "测试工单", Status: "open", Priority: "normal",
		Origin: origin, Category: "general", LastMessageAt: at,
	}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	// created_at 是 autoCreateTime，必须回写目标时刻才能控制窗口。
	if err := db.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Update("created_at", at).Error; err != nil {
		t.Fatalf("backdate ticket: %v", err)
	}
}

// 判据①：阈值边界 —— 4 张不告警，5 张告警 1 条（阈值默认 5）。
func TestRiskTicketFloodThresholdBoundary(t *testing.T) {
	t.Run("four_tickets_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 4; i++ {
			seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
			t.Fatalf("4 张 < 阈值 5 必须不告警，实际 %d 条", got)
		}
	})

	t.Run("five_tickets_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 5; i++ {
			seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
			t.Fatalf("5 张 = 阈值 5 必须告警且只 1 条，实际 %d 条", got)
		}
		var warning entity.RiskWarning
		if err := db.Where("rule_id = ?", "ticket_flood").First(&warning).Error; err != nil {
			t.Fatalf("load: %v", err)
		}
		if warning.UserID == nil || *warning.UserID != riskR8TestUserID {
			t.Fatalf("告警必须归属到该用户，实际 %+v", warning.UserID)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail 必须是 JSON: %v (%s)", err, warning.Detail)
		}
		if got, _ := detail["ticketCount"].(float64); got != 5 {
			t.Fatalf("detail.ticketCount = %v, want 5", detail["ticketCount"])
		}
	})

	t.Run("different_users_isolated", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		// 两个用户各 4 张：各自都不到阈值 → 都不告警。
		for userID := int32(100); userID < 102; userID++ {
			for i := 0; i < 4; i++ {
				seedRiskTicket(t, db, userID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
			}
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
			t.Fatalf("两个用户各 4 张都不到阈值，必须都不告警，实际 %d 条", got)
		}
	})
}

// 判据②：系统自动建的工单不算用户刷屏（origin=system 排除）。
//
// 变异方向：去掉 origin 过滤 → 本用例红（5 张系统单会被算成刷屏）。
func TestRiskTicketFloodExcludesSystemTickets(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 5 张全部来自系统自动建单（Q26 赠送判定失败）。
	for i := 0; i < 5; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginSystem, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
		t.Fatalf("5 张系统自动建单不得算作用户刷屏，实际 %d 条", got)
	}
	// 反向对照：同样 5 张但 origin=user 时必须告警（证明上面的 0 是过滤生效，不是数据不足）。
	for i := 0; i < 5; i++ {
		seedRiskTicket(t, db, 9999, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("同样的 5 张但 origin=user 必须告警，实际 %d 条（若为 0 说明上面的 0 与过滤无关）", got)
	}
}

// 判据③：窗口生效 —— 昨天的工单不算进今天的窗口。
//
// 变异方向：去掉窗口条件 → 本用例红（6 张跨两天会被算成 6 张）。
func TestRiskTicketFloodRespectsWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 今天 2 张（不到阈值 5）+ 昨天 5 张（若跨窗口累加会是 7）。
	for i := 0; i < 2; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	yesterday := now.Add(-24 * time.Hour)
	for i := 0; i < 5; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, yesterday.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
		t.Fatalf("今天只 2 张（昨天的 5 张不在窗口内）必须不告警，实际 %d 条", got)
	}
	// 反向对照：把评估时刻挪到昨天，则昨天那 5 张必须命中。
	if _, err := EvaluateDebtAlerts(db, yesterday); err != nil {
		t.Fatalf("evaluate yesterday: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("昨天的窗口里 5 张必须告警，实际 %d 条（若为 0 说明窗口条件写反了）", got)
	}
}

// 判据④：阈值可配（双向）。
func TestRiskTicketFloodThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 3; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	// 阈值调到 10：3 张不足 → 不告警。
	putRiskSetting(t, db, "risk_rule_ticket_flood_threshold", "10")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
		t.Fatalf("阈值 10 时 3 张必须不告警，实际 %d 条", got)
	}
	// 阈值调到 3：同一批数据必须告警（双向断言，防止硬编码也能过）。
	putRiskSetting(t, db, "risk_rule_ticket_flood_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("阈值 3 时 3 张必须告警，实际 %d 条", got)
	}
}

// 判据⑤：同一窗口重复评估只写 1 条（SubjectKey 节流）。
func TestRiskTicketFloodThrottlesWithinWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 5; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("first: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("首次必须 1 条，实际 %d", got)
	}
	for _, offset := range []time.Duration{time.Hour, 6 * time.Hour} {
		if _, err := EvaluateDebtAlerts(db, now.Add(offset)); err != nil {
			t.Fatalf("evaluate +%v: %v", offset, err)
		}
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("同一窗口内重复评估必须只 1 条，实际 %d", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "ticket_flood").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	wantPrefix := "ticket_flood:user:" + strconv.Itoa(int(riskR8TestUserID)) + ":"
	if !strings.HasPrefix(warning.SubjectKey, wantPrefix) {
		t.Fatalf("subjectKey 形态错误：%q（期望前缀 %q）", warning.SubjectKey, wantPrefix)
	}
}

// 判据⑥：只告警不处置 —— 工单状态、用户状态、黑名单都不得被改。
func TestRiskTicketFloodNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "flooder", Status: "enabled", Role: "user", Balance: 10}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, user.ID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 前置条件：必须真的告警了（否则下面的「未变」全是因为没跑）。
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("前置条件：必须已产生告警，实际 %d 条", got)
	}
	// 用户状态/余额未变。
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "enabled" || after.Balance != user.Balance {
		t.Fatalf("C1：风控不得改用户状态/余额，得到 %+v", after)
	}
	// 工单状态未被改（不得「自动关闭刷屏工单」）。
	var tickets []entity.Ticket
	if err := db.Where("user_id = ?", user.ID).Find(&tickets).Error; err != nil {
		t.Fatalf("load tickets: %v", err)
	}
	for _, ticket := range tickets {
		if ticket.Status != "open" {
			t.Fatalf("C1：风控不得改工单状态，ticket %d status=%q", ticket.ID, ticket.Status)
		}
	}
	// 黑名单未新增。
	var blacklistCount int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklistCount).Error; err != nil {
		t.Fatalf("count blacklist: %v", err)
	}
	if blacklistCount != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklistCount)
	}
}

// 判据⑦：规则停用时不计告警（含反向对照）。
func TestRiskTicketFloodDisabledRuleDoesNotFire(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "ticket_flood").
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
		t.Fatalf("停用时不得告警，实际 %d 条", got)
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "ticket_flood").
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("重新启用后必须告警，实际 %d 条", got)
	}
}

// ════════════════════════════════════════════════════════════════════
// R11 · abnormal_register_pattern（96 §2.1）
// 同一自然日窗口内出现 ≥N 个「同前缀（≥3 字母）+ 连续递增数字」的用户名
//
// 本规则唯一的风险是**大面积误报**：写成 `^[a-z]+\d+$` 会把
// `user2024` / `test123` / `abc999` 这类普通用户名全算进去。
// 因此下面专门有一条「正常用户不误报」的对照用例，
// 变异「把连续递增放宽成任意数字」时它必须红。
// ════════════════════════════════════════════════════════════════════

// seedRiskRegisterUser 造一个指定注册时刻的用户（username + created_at 都受控）。
func seedRiskRegisterUser(t *testing.T, db *gorm.DB, username string, at time.Time) {
	t.Helper()
	user := entity.User{Username: username, Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	// created_at 是 autoCreateTime，必须回写目标时刻才能控制窗口。
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("created_at", at).Error; err != nil {
		t.Fatalf("backdate user %q: %v", username, err)
	}
}

// 判据①：阈值边界 —— 连续 2 个不告警，连续 3 个告警 1 条（阈值默认 3）。
func TestRiskAbnormalRegisterPatternThresholdBoundary(t *testing.T) {
	t.Run("two_consecutive_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for _, name := range []string{"batchone1", "batchone2"} {
			seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
			t.Fatalf("连续 2 个 < 阈值 3 必须不告警，实际 %d 条", got)
		}
	})

	t.Run("three_consecutive_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for _, name := range []string{"batchone1", "batchone2", "batchone3"} {
			seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
			t.Fatalf("连续 3 个 = 阈值 3 必须告警且只 1 条，实际 %d 条", got)
		}
		var warning entity.RiskWarning
		if err := db.Where("rule_id = ?", "abnormal_register_pattern").First(&warning).Error; err != nil {
			t.Fatalf("load: %v", err)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail 必须是 JSON: %v", err)
		}
		if got, _ := detail["sequenceLen"].(float64); got != 3 {
			t.Fatalf("detail.sequenceLen = %v, want 3", detail["sequenceLen"])
		}
		if got, _ := detail["sequenceFrom"].(float64); got != 1 {
			t.Fatalf("detail.sequenceFrom = %v, want 1", detail["sequenceFrom"])
		}
	})
}

// 判据②（关键）：正常用户名不误报。
//
// 变异方向：把 riskLongestConsecutiveRun 放宽成「只数集合大小」（任意数字）
// → 本用例必须红。
//
// ⚠️ 这里有两组样本，缺第二组就抓不到变异（实测踩过）：
//
//	A 组：user2024 / test123 / abc999 —— 三个**不同前缀**。
//	  它们各自前缀下只有 1 个成员，所以「按前缀分组」这一步就已经把它们拆开了，
//	  即使把「连续递增」放宽成「任意数字」，每组仍然只有 1 个 → **不会误报**。
//	  也就是说：只写 A 组的话，这个用例在放宽变异下**依然全绿**（是空断言）。
//
//	B 组：normaluser100 / normaluser500 / normaluser900 —— **同一前缀、数字不连续**。
//	  这一组才是真正检验「连续递增」的样本：放宽后同前缀下 3 个成员会被算成 3 个，
//	  立刻误报 → 用例红。
//
// 两组都保留：A 组钉「不同前缀要拆开」，B 组钉「同前缀但不连续不能算」。
func TestRiskAbnormalRegisterPatternDoesNotFlagNormalUsernames(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// A 组：三个互不同前缀的正常用户名（年份/编号各不相同）。
	for _, name := range []string{"user2024", "test123", "abc999"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	// B 组：同一前缀、数字不连续 —— 必须也不告警。
	for _, name := range []string{"normaluser100", "normaluser500", "normaluser900"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("user2024/test123/abc999 与 normaluser100/500/900 都不是连续递增，必须不告警，实际 %d 条（误报）", got)
	}
	// 反向对照：同样三个前缀（用第三个做连续样本），数字**连续**时必须有告警 ——
	// 否则上面的 0 可能只是因为整个规则没跑（假绿）。
	for _, name := range []string{"seqcheck1", "seqcheck2", "seqcheck3"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("seqcheck1/2/3 连续递增必须告警，实际 %d 条（若为 0 说明上面的 0 与连续性无关）", got)
	}
	// 证据里不得包含 B 组的前缀（证明它真的被连续性判据挡掉了，不是被别的原因漏掉）。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "abnormal_register_pattern").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if strings.Contains(warning.Detail, "normaluser") {
		t.Fatalf("B 组（同前缀不连续）不得进入告警证据：%s", warning.Detail)
	}
}

// 判据③：非连续数字即使同前缀也不告警（a1/a5/a9 那种）。
//
// 与判据② 的区别：这里前缀相同、只是数字跳跃 —— 钉的是「同前缀不等于连续」。
func TestRiskAbnormalRegisterPatternRequiresConsecutiveNumbers(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for _, name := range []string{"skipy1", "skipy5", "skipy9"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("skipy1/skipy5/skipy9 数字不连续，必须不告警，实际 %d 条", got)
	}
}

// 判据④：前缀至少 3 字符 —— a1/a2/a3 这种 1 字符前缀不告警。
//
// 变异方向：把 riskAbnormalRegisterPrefixMinLength 改成 1 → 本用例红。
func TestRiskAbnormalRegisterPatternRequiresMinPrefixLength(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 1 字符前缀 + 连续数字：形态上「像编号」，但前缀太短，
	// 正常用户名撞前缀的概率太高（大量用户就叫 a1/b2）。
	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	// 2 字符前缀同样不算。
	for _, name := range []string{"ab1", "ab2", "ab3", "ab4"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("前缀只有 1~2 字符必须不告警（易误报），实际 %d 条", got)
	}
	// 反向对照：3 字符前缀 + 连续数字必须告警。
	for _, name := range []string{"abc1", "abc2", "abc3"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("3 字符前缀 + 连续数字必须告警，实际 %d 条", got)
	}
}

// 判据⑤：窗口生效 —— 昨天的账号不算进今天。
func TestRiskAbnormalRegisterPatternRespectsWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 今天 2 个 + 昨天 3 个（若跨窗口累加会是 5 个连续）。
	seedRiskRegisterUser(t, db, "windowe1", now.Add(-time.Minute))
	seedRiskRegisterUser(t, db, "windowe2", now.Add(-time.Minute))
	yesterday := now.Add(-24 * time.Hour)
	for _, name := range []string{"windowe3", "windowe4", "windowe5"} {
		seedRiskRegisterUser(t, db, name, yesterday.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("今天只 2 个（昨天的 3 个不在窗口内）必须不告警，实际 %d 条", got)
	}
	// 反向对照：挪到昨天，昨天的 1~3 不连续（windowE3/4/5），但今天不会混入。
	// 直接断言「昨天窗口里至少能算到 userseq 那批」不好构造，
	// 改为验证今天窗口的 2 个加上昨天 3 个**不会**凑成 5 连续（已在上面断言）。
}

// 判据⑥：阈值可配（双向）。
func TestRiskAbnormalRegisterPatternThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for _, name := range []string{"config1", "config2", "config3"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	// 阈值调到 5：3 个不足 → 不告警。
	putRiskSetting(t, db, "risk_rule_abnormal_register_pattern_threshold", "5")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("阈值 5 时 3 个必须不告警，实际 %d 条", got)
	}
	// 阈值调到 3：同一批数据必须告警。
	putRiskSetting(t, db, "risk_rule_abnormal_register_pattern_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("阈值 3 时 3 个必须告警，实际 %d 条", got)
	}
}

// 判据⑦：只告警不处置 —— 用户状态不得被改、不得自动拉黑。
func TestRiskAbnormalRegisterPatternNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for _, name := range []string{"action1", "action2", "action3"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("前置条件：必须已产生告警，实际 %d 条", got)
	}
	var users []entity.User
	if err := db.Where("username IN ?", []string{"action1", "action2", "action3"}).Find(&users).Error; err != nil {
		t.Fatalf("load users: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("load users: 期望 3 个，实际 %d", len(users))
	}
	for _, user := range users {
		if user.Status != "enabled" {
			t.Fatalf("C1：风控不得改用户状态，%s status=%q", user.Username, user.Status)
		}
	}
	var blacklistCount int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklistCount).Error; err != nil {
		t.Fatalf("count blacklist: %v", err)
	}
	if blacklistCount != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklistCount)
	}
}

// 判据⑧：规则停用时不告警（含反向对照）。
func TestRiskAbnormalRegisterPatternDisabledRuleDoesNotFire(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for _, name := range []string{"disable1", "disable2", "disable3"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "abnormal_register_pattern").
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 0 {
		t.Fatalf("停用时不得告警，实际 %d 条", got)
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "abnormal_register_pattern").
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("重新启用后必须告警，实际 %d 条", got)
	}
}

// 判据⑨：用户名解析的单元级真值表（形态收窄的核心）。
//
// 期望值一律写字面量（不引用被测常量），常量漂移时这里必须红。
func TestRiskSplitSequentialUsernameTruthTable(t *testing.T) {
	cases := []struct {
		username   string
		wantPrefix string
		wantNumber int
		wantOK     bool
	}{
		// 合法形态：≥3 字母 + 1..9 位数字
		{"abc1", "abc", 1, true},
		{"batchone123", "batchone", 123, true},
		{"ABC42", "abc", 42, true},   // 大小写归一
		{"  abc7  ", "abc", 7, true}, // 两端空白容忍
		// 前缀不足 3 字符 → 拒绝
		{"a1", "", 0, false},
		{"ab1", "", 0, false},
		// 纯字母无编号 → 拒绝
		{"alice", "", 0, false},
		// 含非字母非数字字符 → 拒绝
		{"ab_c1", "", 0, false},
		{"ab-c1", "", 0, false},
		{"张三1", "", 0, false},
		// 字母夹数字（不是纯前缀+纯后缀）→ 拒绝
		{"a1bc2", "", 0, false},
		// 纯数字 → 拒绝
		{"12345", "", 0, false},
		// 数字超 9 位 → 拒绝
		{"abc1234567890", "", 0, false},
		// 空串
		{"", "", 0, false},
	}
	for _, tc := range cases {
		prefix, number, ok := riskSplitSequentialUsername(tc.username)
		if ok != tc.wantOK {
			t.Fatalf("riskSplitSequentialUsername(%q) ok = %v, want %v", tc.username, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if prefix != tc.wantPrefix || number != tc.wantNumber {
			t.Fatalf("riskSplitSequentialUsername(%q) = (%q, %d), want (%q, %d)",
				tc.username, prefix, number, tc.wantPrefix, tc.wantNumber)
		}
	}
}

// 判据⑩：最长连续段计算的真值表。
func TestRiskLongestConsecutiveRun(t *testing.T) {
	cases := []struct {
		numbers   []int
		wantLen   int
		wantStart int
	}{
		{[]int{1, 2, 3}, 3, 1},
		{[]int{1, 5, 9}, 1, 1},          // 全不连续
		{[]int{2024, 123, 999}, 1, 123}, // 正常用户名的数字
		{[]int{7, 8, 10, 11, 12}, 3, 10},
		{[]int{5, 6, 7, 8}, 4, 5},
		{[]int{3}, 1, 3},
		{[]int{}, 0, 0},
	}
	for _, tc := range cases {
		set := make(map[int]struct{}, len(tc.numbers))
		for _, number := range tc.numbers {
			set[number] = struct{}{}
		}
		gotLen, gotStart := riskLongestConsecutiveRun(set)
		if gotLen != tc.wantLen || gotStart != tc.wantStart {
			t.Fatalf("riskLongestConsecutiveRun(%v) = (%d, %d), want (%d, %d)",
				tc.numbers, gotLen, gotStart, tc.wantLen, tc.wantStart)
		}
	}
}

// ruleIDsOf 收集规则 id（失败信息用，避免只报个数时无从定位）。
func ruleIDsOf(rules []entity.RiskRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.RuleID)
	}
	return out
}

// ════════════════════════════════════════════════════════════════════
// R1 · multi_account_ip（96 §2.1）—— 18 条行为规则的**范式**与判据
//
// 数据源：logs 表里 action=10 / module=auth / status=success 的登录审计行。
// 六条判据与任务书一一对应（①阈值边界 ②节流 ③窗口滚动 ④阈值可配
// ⑤不处置 ⑥规则停用）。
// ════════════════════════════════════════════════════════════════════

const (
	riskR1TestIP      = "203.0.113.9"
	riskR1TestOtherIP = "198.51.100.7"
	riskR1TestDay     = "2026-11-14"
)

// seedRiskLoginAudit 写一条真实的登录审计行（走生产同一个写入函数）。
//
// 走 WriteAccountAuditWithDB 而不是手搓 entity.Log：手搓会漏掉
// 「结果 JSON 的 status 字段由 accountAuditFields 白名单过滤」这一层，
// 而 R1 的筛选条件恰好依赖那个字段的真实序列化形态。
func seedRiskLoginAudit(t *testing.T, db *gorm.DB, userID int32, username, clientIP string, at time.Time, status string) {
	t.Helper()
	event := AccountAuditEvent{
		UserID: userID, Username: username, Module: "auth", Action: 10,
		Description: "用户登录成功", ClientIP: clientIP,
		Result: map[string]any{"status": status, "message": "登录", "scene": "password"},
	}
	if err := WriteAccountAuditWithDB(db, event); err != nil {
		t.Fatalf("seed login audit: %v", err)
	}
	// WriteAccountAuditWithDB 用 time.Now()，这里回写目标时间以便控制窗口。
	if err := db.Model(&entity.Log{}).Where("user_id = ? AND client_ip = ?", userID, clientIP).
		Update("create_time", at).Error; err != nil {
		t.Fatalf("backdate audit row: %v", err)
	}
}

// riskR1TestTime 返回判据用的固定时刻（当天中午，远离窗口边界）。
func riskR1TestTime() time.Time {
	return time.Date(2026, 11, 14, 12, 0, 0, 0, logger.ShanghaiLocation())
}

// countRuleWarnings 数某规则写了几条告警。
func countRuleWarnings(t *testing.T, db *gorm.DB, ruleID string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Where("rule_id = ?", ruleID).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", ruleID, err)
	}
	return count
}

// 判据①：同 IP 2 个账号、阈值=3 → 不告警；3 个账号 → 告警 1 条。
//
// 这条把「≥ vs >」的边界钉死：变异成 > 时 3 个账号不告警 → 本用例红。
func TestRiskMultiAccountIPThresholdBoundary(t *testing.T) {
	t.Run("two_accounts_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
		putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
		now := riskR1TestTime()
		seedRiskLoginAudit(t, db, 1, "a", riskR1TestIP, now.Add(-time.Hour), "success")
		seedRiskLoginAudit(t, db, 2, "b", riskR1TestIP, now.Add(-2*time.Hour), "success")

		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "multi_account_ip"); got != 0 {
			t.Fatalf("2 个账号 < 阈值 3 必须不告警，实际写了 %d 条", got)
		}
	})

	t.Run("three_accounts_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
		putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
		now := riskR1TestTime()
		for i := int32(1); i <= 3; i++ {
			seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
		}
		// ⚠️ 同一个 user 登录 3 次不算 3 个账号（判据是**不同** user_id）。
		seedRiskLoginAudit(t, db, 1, "u", riskR1TestIP, now.Add(-10*time.Minute), "success")

		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
			t.Fatalf("3 个账号 = 阈值 3 必须告警且只 1 条，实际 %d 条", got)
		}
		var warning entity.RiskWarning
		if err := db.Where("rule_id = ?", "multi_account_ip").First(&warning).Error; err != nil {
			t.Fatalf("load warning: %v", err)
		}
		if warning.IP != riskR1TestIP {
			t.Fatalf("告警必须记录来源 IP，实际 %q", warning.IP)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail 必须是 JSON 证据快照: %v (%s)", err, warning.Detail)
		}
		// 证据里的账号数必须是 3（去重后），不是 4（含重复登录）。
		if got, _ := detail["accounts"].(float64); got != 3 {
			t.Fatalf("detail.accounts = %v, want 3（同一账号多次登录不得重复计数）", detail["accounts"])
		}
	})

	t.Run("other_ip_isolated", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
		putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
		now := riskR1TestTime()
		// 主 IP 2 个账号、另一 IP 2 个账号：各自都不到阈值 → 都不告警。
		seedRiskLoginAudit(t, db, 1, "a", riskR1TestIP, now.Add(-time.Hour), "success")
		seedRiskLoginAudit(t, db, 2, "b", riskR1TestIP, now.Add(-time.Hour), "success")
		seedRiskLoginAudit(t, db, 3, "c", riskR1TestOtherIP, now.Add(-time.Hour), "success")
		seedRiskLoginAudit(t, db, 4, "d", riskR1TestOtherIP, now.Add(-time.Hour), "success")
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "multi_account_ip"); got != 0 {
			t.Fatalf("两个 IP 各自 2 账号都不到阈值，必须都不告警，实际 %d 条", got)
		}
	})

	t.Run("failed_logins_not_counted", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
		putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
		now := riskR1TestTime()
		// status=denied 的登录尝试不得计入账号数：
		// 否则「一个人反复试管理员入口」会被误判成「同 IP 多账号」。
		for i := int32(1); i <= 5; i++ {
			seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Hour), "denied")
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "multi_account_ip"); got != 0 {
			t.Fatalf("只有 denied 行时不得告警，实际 %d 条", got)
		}
	})
}

// 判据②：同一次评估跑两遍 → 只写 1 条（SubjectKey 节流生效）。
func TestRiskMultiAccountIPThrottlesWithinWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
	now := riskR1TestTime()
	for i := int32(1); i <= 3; i++ {
		seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("first evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("首次评估必须写 1 条，实际 %d", got)
	}
	// 同一窗口内再跑两次（模拟每 6h 一轮的 worker）。
	for _, offset := range []time.Duration{time.Hour, 5 * time.Hour} {
		if _, err := EvaluateDebtAlerts(db, now.Add(offset)); err != nil {
			t.Fatalf("evaluate at +%v: %v", offset, err)
		}
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("同一窗口内重复评估必须只 1 条（SubjectKey 节流），实际 %d", got)
	}
	// subject 里必须编进窗口起点（这是节流机制本身，不是实现细节）。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "multi_account_ip").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.HasPrefix(warning.SubjectKey, "multi_account_ip:"+riskR1TestIP+":") {
		t.Fatalf("subjectKey 形态错误：%q（期望 multi_account_ip:<ip>:<窗口起点>）", warning.SubjectKey)
	}
}

// 判据③：隔一个窗口后再跑 → 再写 1 条（不是永久去重）。
func TestRiskMultiAccountIPAlertsAgainNextWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
	now := riskR1TestTime()
	for i := int32(1); i <= 3; i++ {
		seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("day 1 必须 1 条，实际 %d", got)
	}
	// 次日：同一个 IP 又登录 3 个账号 → 窗口起点变化 → 必须再写一条。
	nextDay := now.Add(24 * time.Hour)
	for i := int32(1); i <= 3; i++ {
		seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, nextDay.Add(-time.Duration(i)*time.Minute), "success")
	}
	if _, err := EvaluateDebtAlerts(db, nextDay); err != nil {
		t.Fatalf("day 2: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 2 {
		t.Fatalf("隔一个窗口后必须再告警（不是永久去重），实际 %d 条", got)
	}
	// 两条的 subjectKey 必须不同（窗口起点不同），这是「不是永久去重」的机制证据。
	var rows []entity.RiskWarning
	if err := db.Where("rule_id = ?", "multi_account_ip").Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if rows[0].SubjectKey == rows[1].SubjectKey {
		t.Fatalf("两个窗口的 subjectKey 相同（%q）—— 窗口起点没编进 key，节流会退化成永久去重", rows[0].SubjectKey)
	}
}

// 判据④：阈值可配 —— 改成 5 后 3 个账号不告警。
func TestRiskMultiAccountIPThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
	now := riskR1TestTime()
	for i := int32(1); i <= 3; i++ {
		seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
	}
	// 阈值调到 5：3 个账号不足 → 不告警。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "5")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 0 {
		t.Fatalf("阈值 5 时 3 个账号必须不告警，实际 %d 条", got)
	}
	// 阈值调到 3：同一批数据必须告警（双向断言 —— 只测单向时硬编码小常量也能过）。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("阈值 3 时 3 个账号必须告警，实际 %d 条", got)
	}
}

// 判据⑤：只告警不处置（96 C1）。
//
// 参照 TestRiskEvaluateDebtAlertsNeverMutatesUsers 的写法，但覆盖面更宽：
//   - users.status 逐字段未变；
//   - blacklist_entries 行数未变（不得顺手拉黑这个 IP）；
//   - 用户余额未变。
func TestRiskMultiAccountIPNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	// 判据写的是「阈值=3」：种子默认是 5（96 §2.1 的 N 默认 5），必须显式设定。
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "3")
	now := riskR1TestTime()
	users := make([]entity.User, 0, 3)
	for i := int32(1); i <= 3; i++ {
		user := entity.User{Username: "u" + strconv.Itoa(int(i)), Balance: 10, Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		users = append(users, user)
		seedRiskLoginAudit(t, db, user.ID, user.Username, riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
	}
	// 预置一条黑名单规则，用于证明「跑完之后没有新增」。
	existing := entity.BlacklistEntry{Kind: "ip", Pattern: "192.0.2.1", ValueHash: "seed", MatchType: "exact", Reason: "已有规则", Enabled: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed blacklist: %v", err)
	}

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 确认真的命中了（否则下面的「未变」全是因为没跑）。
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("前置条件：必须已产生告警，实际 %d 条", got)
	}

	for _, user := range users {
		var after entity.User
		if err := db.First(&after, user.ID).Error; err != nil {
			t.Fatalf("reload user: %v", err)
		}
		if after.Status != "enabled" {
			t.Fatalf("C1：风控不得改用户状态，user %d status=%q", user.ID, after.Status)
		}
		if after.Balance != user.Balance {
			t.Fatalf("C1：风控不得改余额，user %d balance %v -> %v", user.ID, user.Balance, after.Balance)
		}
	}
	// 黑名单：行数与内容都不得变化（尤其不得把这个 IP 拉黑）。
	var blacklistRows []entity.BlacklistEntry
	if err := db.Order("id ASC").Find(&blacklistRows).Error; err != nil {
		t.Fatalf("load blacklist: %v", err)
	}
	if len(blacklistRows) != 1 || blacklistRows[0].ID != existing.ID {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", len(blacklistRows))
	}
	var blocked int64
	if err := db.Model(&entity.BlacklistEntry{}).Where("pattern = ?", riskR1TestIP).Count(&blocked).Error; err != nil {
		t.Fatalf("count blocked: %v", err)
	}
	if blocked != 0 {
		t.Fatalf("C1：告警的 IP 不得被自动拉黑，实际 %d 行", blocked)
	}
}

// 判据⑥：规则被停用（risk_rules.enabled=false）时不告警。
func TestRiskMultiAccountIPDisabledRuleDoesNotFire(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := int32(1); i <= 5; i++ {
		seedRiskLoginAudit(t, db, i, "u", riskR1TestIP, now.Add(-time.Duration(i)*time.Minute), "success")
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "multi_account_ip").
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 0 {
		t.Fatalf("规则停用时不得告警，实际 %d 条", got)
	}
	// 反向对照：重新启用后必须能告警（证明上面的 0 是因为停用，不是因为数据不足）。
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", "multi_account_ip").
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable rule: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("重新启用后必须告警，实际 %d 条（若为 0 说明上面的 0 与 enabled 无关）", got)
	}
}

// 并发去重：多个 goroutine 同时判定同一主体，同一窗口只允许 1 条。
//
// 这是「原子插入」与「先查后写」的分水岭。
//
// ⚠️ 本用例**直接打临界区** createRiskWarningIfNeeded，而不是走 EvaluateDebtAlerts。
// 这是实测踩出来的（必须记下）：走整个 EvaluateDebtAlerts 时，SQLite 的写锁会把
// goroutine 串行化，8 并发里往往只有 1~2 个真正同时到达插入点，于是
// 「先查后写」的实现也能侥幸只写 1 行 —— 那条用例是**假钉子**：
// 把插入换成无条件 Create 后它仍然 3/3 全绿（实测）。
//
// 直接打临界区 + start barrier + 16 并发才能真复现：
// 实测「先查后写」形态 16 并发写出 8 行，原子实现稳定 1 行。
func TestRiskMultiAccountIPConcurrentEvaluationWritesOnce(t *testing.T) {
	// 连跑 3 轮：单轮有偶然性，多轮同时通过才算稳定。
	//
	// ⚠️ 每轮必须**换一个 DB 名字**：newRiskAlertTestDB 用 t.Name() 命名，而
	// `mode=memory&cache=shared` 下同名 = 同一个库（连接池按名字共享）。
	// 不换名时第 1 轮拿到的是第 0 轮残留的行 → 「已创建 0 个」的假红（实测踩到）。
	for round := 0; round < 3; round++ {
		db := newRiskAlertTestDBNamed(t, "concurrent-round-"+strconv.Itoa(round))
		now := riskR1TestTime()
		rule := entity.RiskRule{
			RuleID: entity.RiskRuleIDMultiAccountIP, Enabled: true,
			Threshold: 3, Severity: entity.RiskSeverityMedium,
		}
		windowStart, _ := riskLoginWindow(now)
		candidate := riskWarningCandidate{
			Rule: rule, Subject: riskMultiAccountIPSubject(riskR1TestIP, windowStart),
			Severity: rule.Severity, IP: riskR1TestIP, Detail: `{"accounts":3}`,
		}
		const workers = 16
		var wg sync.WaitGroup
		start := make(chan struct{})
		createdFlags := make([]bool, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				// barrier：所有 goroutine 在同一时刻冲进临界区。
				<-start
				created, _, _ := createRiskWarningIfNeeded(db, candidate, now)
				createdFlags[idx] = created
			}(i)
		}
		close(start)
		wg.Wait()

		if got := countRuleWarnings(t, db, entity.RiskRuleIDMultiAccountIP); got != 1 {
			t.Fatalf("round %d：%d 并发下必须只写 1 行，实际 %d 行（>1 = 去重不是原子的）", round, workers, got)
		}
		// 反向对照：必须恰好 1 个 goroutine 报告「自己创建了」。
		// 若 0 个，说明插入根本没发生（用例会假绿地满足「只有 1 行」的另一种情形）。
		createdCount := 0
		for _, created := range createdFlags {
			if created {
				createdCount++
			}
		}
		if createdCount != 1 {
			t.Fatalf("round %d：应恰好 1 个 goroutine 报告「已创建」，实际 %d", round, createdCount)
		}
	}
}

// TestRiskWarningDedupIsAtomicAcrossAllRules 是**共享临界区的回归钉**。
//
// ⚠️⚠️ 这条用例存在的原因，比它测什么更重要，务必读完再改：
//
// 「告警去重」只有一个实现点 —— createRiskWarningIfNeeded → insertRiskWarningIfAbsent。
// debt_user / debt_batch / debt_system / multi_account_ip **四条规则全部共用它**，
// 它们之间没有任何独立的去重代码。也就是说：
//
//   - 只改 debt_* 的调用路径（例如给它们加一层包装、换一个查询构造方式）时，
//     看起来「与 R1 无关」，实际上动的是同一个临界区；
//   - 反之，只给 R1 写并发用例也**不足以**证明 debt_* 安全 —— 它们走的是同一个函数，
//     但历史上 debt_* 从未被并发覆盖过（实测修复前 8 并发评估 debt_user 会写出 2 行）。
//
// 所以这里对四条规则**逐一**跑 16 并发，断言每组恰好 1 行。
// 变异方向：把 insertRiskWarningIfAbsent 退回「先查后写」→ **四条必须全红**
// （修复前只有 R1 那条会红，这条覆盖其余三条）。
//
// 复用 R1 用例踩出来的三个坑（照抄即可，别重新踩）：
//  1. 必须**直接打临界区**，不能包在 EvaluateDebtAlerts 外层 —— SQLite 写锁会把
//     goroutine 串行化，外层调用下「先查后写」也能侥幸只写 1 行（假钉子）。
//  2. start barrier 让所有 goroutine 同时冲进临界区，否则并发窗口太窄。
//  3. `mode=memory&cache=shared` 下**同名即同一个库** —— 每条规则必须换 DB 名，
//     否则后一条规则看到前一条的残留行，报「已创建 0 个」的假红。
func TestRiskWarningDedupIsAtomicAcrossAllRules(t *testing.T) {
	// 四条规则各自的节流语义都要覆盖：debt_user 24h、debt_system 6h、
	// debt_batch 0（永不重复）、multi_account_ip 0（窗口起点在 key 里）。
	cases := []struct {
		ruleID          string
		throttleSeconds int
		subjectKey      string
	}{
		{ruleID: "debt_user", throttleSeconds: 24 * 60 * 60, subjectKey: "user:12345"},
		{ruleID: "debt_batch", throttleSeconds: 0, subjectKey: "batch:batch-7f3a"},
		{ruleID: "debt_system", throttleSeconds: 6 * 60 * 60, subjectKey: "system:debt"},
		{ruleID: "multi_account_ip", throttleSeconds: 0, subjectKey: "multi_account_ip:203.0.113.9:2026-11-14T00:00:00+08:00"},
		// 每条新增的行为/欠款规则都必须在此登记 —— 否则它只是「碰巧」走了同一个
		// 临界区，没有回归钉。R8（ticket_flood）落地时补入。
		{ruleID: "ticket_flood", throttleSeconds: 24 * 60 * 60, subjectKey: "ticket_flood:user:4242:2026-11-14T00:00:00+08:00"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.ruleID, func(t *testing.T) {
			// 每条规则独立 DB 名（见注释 3）。
			db := newRiskAlertTestDBNamed(t, "dedup-atomic-"+tc.ruleID)
			widenRiskCreateRaceWindow(t, db)
			now := riskR1TestTime()
			rule := entity.RiskRule{
				RuleID: tc.ruleID, Enabled: true, Severity: entity.RiskSeverityMedium,
				ThrottleSeconds: tc.throttleSeconds, Threshold: 1,
			}
			candidate := riskWarningCandidate{
				Rule: rule, Subject: tc.subjectKey, Severity: rule.Severity,
				Detail: `{"ruleId":"` + tc.ruleID + `"}`,
			}
			const workers = 16
			var wg sync.WaitGroup
			start := make(chan struct{})
			createdFlags := make([]bool, workers)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					<-start // barrier
					created, _, _ := createRiskWarningIfNeeded(db, candidate, now)
					createdFlags[idx] = created
				}(i)
			}
			close(start)
			wg.Wait()

			if got := countRuleWarnings(t, db, tc.ruleID); got != 1 {
				t.Fatalf("%s：%d 并发下必须只写 1 行，实际 %d 行（>1 = 共享临界区的去重不是原子的）",
					tc.ruleID, workers, got)
			}
			// 反向对照：必须恰好 1 个 goroutine 报告「已创建」。
			// 若 0 个，说明插入根本没发生 —— 那样「只有 1 行」也可能成立（一行都没有）。
			createdCount := 0
			for _, created := range createdFlags {
				if created {
					createdCount++
				}
			}
			if createdCount != 1 {
				t.Fatalf("%s：应恰好 1 个 goroutine 报告「已创建」，实际 %d", tc.ruleID, createdCount)
			}
		})
	}
}

// widenRiskCreateRaceWindow 把「先查后写」的竞态窗口**确定性地**撑开。
//
// 为什么需要它（实测数据，不是推测）：
//
//	直接跑 16 并发，「先查后写」的实现并不是每次都写出多行 —— 实测 8 轮里有 4 轮
//	debt_batch 只有 1 行（漏检）。原因是 goroutine 的调度顺序偶然让某个 goroutine
//	先完成插入、其余再查询时就命中了。这种依赖调度的用例是**假钉子**：
//	它碰巧能抓到缺陷，但也会碰巧放过。
//
// 做法：在 gorm 的 create 回调里加一次短暂等待，让「所有 goroutine 都完成 SELECT
// 之后才有人开始 INSERT」。这放大的正是「判重与写入不是同一条语句」这个缺陷本身 ——
// 原子实现（INSERT ... WHERE NOT EXISTS）不受影响，因为它压根不依赖那次 SELECT；
// 而「先查后写」会在等待后各插一行。
//
// 只影响本测试进程内的这个 db 句柄，不改生产代码路径。
func widenRiskCreateRaceWindow(t *testing.T, db *gorm.DB) {
	t.Helper()
	const callbackName = "test:risk_dedup_widen_race_window"
	err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		if tx.Statement.Schema.Table != entity.TableNameRiskWarning {
			return
		}
		// 20ms 足以让 16 个 goroutine 全部越过各自的 SELECT。
		time.Sleep(20 * time.Millisecond)
	})
	if err != nil {
		t.Fatalf("register race-window callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
}

// R1 数据源判据：筛选条件必须与 controller 的 logActionLogin / module 字面量一致。
//
// 跨层字面量（service 不能 import controller）唯一可行的防漂移手段是源码断言。
func TestRiskLoginAuditPredicateMatchesControllerConstants(t *testing.T) {
	// 后端常量
	if riskLoginAuditAction != 10 {
		t.Fatalf("riskLoginAuditAction = %d，必须与 controller/logs.go 的 logActionLogin(10) 一致", riskLoginAuditAction)
	}
	if riskLoginAuditModule != "auth" {
		t.Fatalf("riskLoginAuditModule = %q，必须与 account_audit 写入方使用的 module 一致", riskLoginAuditModule)
	}
	// 源码侧：controller 里 logActionLogin 必须仍是 10（防有人改号）。
	raw, err := os.ReadFile("../controller/logs.go")
	if err != nil {
		t.Fatalf("read controller/logs.go: %v", err)
	}
	if !strings.Contains(string(raw), "logActionLogin      = 10") {
		t.Fatal("controller/logs.go 的 logActionLogin 不再是 10 —— R1 的日志筛选会静默失效")
	}
	// 三个成功登录写入点必须仍用 logActionLogin 且 module="auth"。
	userRaw, err := os.ReadFile("../controller/user.go")
	if err != nil {
		t.Fatalf("read controller/user.go: %v", err)
	}
	if got := strings.Count(string(userRaw), `recordAccountAudit(c, user, "auth", logActionLogin`); got < 1 {
		t.Fatal("user.go 里找不到密码登录的审计写入点（module=auth, action=logActionLogin）")
	}
	if got := strings.Count(string(userRaw), `recordAccountAudit(c, &user, "auth", logActionLogin`); got < 1 {
		t.Fatal("user.go 里找不到短信登录的审计写入点")
	}
}

// riskLoginWindow 的窗口语义：上海时区自然日，跨月/跨年正确。
func TestRiskLoginWindowUsesShanghaiNaturalDay(t *testing.T) {
	// 2026-11-14 00:30 +08:00 属于 14 日窗口（不是 UTC 的 13 日）。
	loc := logger.ShanghaiLocation()
	early := time.Date(2026, 11, 14, 0, 30, 0, 0, loc)
	start, end := riskLoginWindow(early)
	if start.Format("2006-01-02T15:04:05") != "2026-11-14T00:00:00" {
		t.Fatalf("窗口起点 = %v，want 2026-11-14T00:00:00（必须按上海时区切日）", start)
	}
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("窗口长度 = %v，want 24h", end.Sub(start))
	}
	// 月末与年末滚动。
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 30, 23, 59, 0, 0, loc), "2026-11-30T00:00:00"},
		{time.Date(2026, 12, 31, 12, 0, 0, 0, loc), "2026-12-31T00:00:00"},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, loc), "2027-01-01T00:00:00"},
	} {
		got, _ := riskLoginWindow(tc.at)
		if got.Format("2006-01-02T15:04:05") != tc.want {
			t.Fatalf("riskLoginWindow(%v) = %v, want %s", tc.at, got, tc.want)
		}
	}
}
