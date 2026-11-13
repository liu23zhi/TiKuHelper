package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	riskControllerTestRiskWebhook   = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=risk-ctrl"
	riskControllerTestTicketWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=ticket-ctrl"
)

func newRiskControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:risk-controller-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// ⚠️ AutoMigrate 必须**逐个模型分次调用**：一次传多个模型时 GORM 会把建表语句
	// 合并，导致物理表列缺失，而报错信息完全不会提到 AutoMigrate（队长广播事故③）。
	for _, model := range []any{
		&entity.SystemSetting{},
		&entity.TicketNotification{},
		&entity.RiskRule{},
		&entity.RiskWarning{},
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

func withRiskControllerDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := riskAdminDB
	riskAdminDB = func() *gorm.DB { return db }
	t.Cleanup(func() { riskAdminDB = previous })
}

// performRiskRequest 走真实 handler（含 requireAdmin 的二次校验）。
func performRiskRequest(t *testing.T, user *entity.User, method, target, body string, params gin.Params, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return performTicketControllerRequest(t, user, method, target, body, params, handler)
}

func readRiskBody(t *testing.T, resp *httptest.ResponseRecorder) string {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return ""
	}
	return resp.Body.String()
}

// riskTestItoa 是本文件内的小工具（controller 包里没有全局 itoa）。
func riskTestItoa(value int64) string { return strconv.FormatInt(value, 10) }

// 四条路由全部要求管理员：普通用户必须 403，未登录必须 401。
//
// 这条用例同时覆盖 98-数据源修正与画像权限约束.md 的「每个 handler 内部二次校验角色」
// —— 即使路由层漏挂 RequireAdmin，这里的 403 也会抓到。
func TestRiskControllersRequireAdmin(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	handlers := map[string]gin.HandlerFunc{
		"list":              RiskWarningList,
		"detail":            RiskWarningDetail,
		"webhook read":      RiskWecomSettings,
		"webhook update":    UpdateRiskWecomSettings,
		"webhook send test": TestRiskWecomWebhook,
	}
	for name, handler := range handlers {
		t.Run(name+"/user_forbidden", func(t *testing.T) {
			resp := performRiskRequest(t, &entity.User{ID: 1, Role: "user"}, http.MethodGet, "/risk/alerts", "", nil, handler)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
			}
		})
		t.Run(name+"/anonymous_unauthorized", func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := performRiskRequestNoUser(t, "/risk/alerts", handler)
			if recorder != http.StatusUnauthorized {
				t.Fatalf("status=%d", recorder)
			}
		})
	}
}

func performRiskRequestNoUser(t *testing.T, target string, handler gin.HandlerFunc) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := performTicketControllerRequest(t, nil, http.MethodGet, target, "", nil, handler)
	return recorder.Code
}

// 告警列表：只读、分页、筛选；非法时间/参数必须 400 且文案指明是哪一项。
func TestRiskWarningListController(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	now := time.Now()
	for i := 0; i < 3; i++ {
		row := entity.RiskWarning{
			RuleID:     entity.RiskRuleIDDebtUser,
			SubjectKey: "user:" + riskTestItoa(int64(i+1)),
			Severity:   entity.RiskSeverityMedium,
			CreatedAt:  now.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	admin := &entity.User{ID: 99, Role: "admin"}
	resp := performRiskRequest(t, admin, http.MethodGet, "/risk/alerts?page=1&pageSize=2", "", nil, RiskWarningList)
	body := readRiskBody(t, resp)
	var list struct {
		Data     []entity.RiskWarning `json:"data"`
		Total    int64                `json:"total"`
		Page     int                  `json:"page"`
		PageSize int                  `json:"pageSize"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if resp.Code != http.StatusOK || list.Total != 3 || len(list.Data) != 2 || list.PageSize != 2 {
		t.Fatalf("status=%d body=%s", resp.Code, body)
	}
	// 非法 notified 值 → 400 且指明参数名（前端要知道改哪个控件）。
	resp = performRiskRequest(t, admin, http.MethodGet, "/risk/alerts?notified=maybe", "", nil, RiskWarningList)
	if resp.Code != http.StatusBadRequest || !strings.Contains(readRiskBody(t, resp), "notified") {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	// 反向时间范围 → 400。
	resp = performRiskRequest(t, admin, http.MethodGet,
		"/risk/alerts?createdFrom=2026-11-13&createdTo=2026-11-12", "", nil, RiskWarningList)
	if resp.Code != http.StatusBadRequest || !strings.Contains(readRiskBody(t, resp), "创建时间范围无效") {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	// 非法 userId → 400。
	resp = performRiskRequest(t, admin, http.MethodGet, "/risk/alerts?userId=abc", "", nil, RiskWarningList)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	// 正常时间范围（含当天整天）：createdTo=今天 必须能覆盖今天创建的行。
	today := time.Now().Format("2006-01-02")
	resp = performRiskRequest(t, admin, http.MethodGet, "/risk/alerts?createdFrom="+today+"&createdTo="+today, "", nil, RiskWarningList)
	if resp.Code != http.StatusOK || !strings.Contains(readRiskBody(t, resp), `"total":3`) {
		t.Fatalf("date-only range must cover the whole day, status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
}

// 详情：非法 id → 400，不存在 → 404，正常 → 200。
func TestRiskWarningDetailController(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	row := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtBatch, SubjectKey: "batch:x", Severity: entity.RiskSeverityHigh, Detail: `{"debtAmount":1}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	admin := &entity.User{ID: 1, Role: "admin"}
	params := gin.Params{{Key: "id", Value: "abc"}}
	if resp := performRiskRequest(t, admin, http.MethodGet, "/risk/alerts/abc", "", params, RiskWarningDetail); resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.Code)
	}
	params = gin.Params{{Key: "id", Value: "99999"}}
	if resp := performRiskRequest(t, admin, http.MethodGet, "/risk/alerts/99999", "", params, RiskWarningDetail); resp.Code != http.StatusNotFound {
		t.Fatalf("missing row must be 404, status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	params = gin.Params{{Key: "id", Value: riskTestItoa(row.ID)}}
	resp := performRiskRequest(t, admin, http.MethodGet, "/risk/alerts/1", "", params, RiskWarningDetail)
	if resp.Code != http.StatusOK || !strings.Contains(readRiskBody(t, resp), "batch:x") {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
}

// webhook 读写：响应里 URL 恒为空 + 脱敏预览；空白/掩码更新保持原值；清除只清自己那条。
//
// ⚠️ 这是 96 §0 C3 在 controller 层的落地判据：
// 通过 HTTP 写一遍风控配置，工单那三行必须**逐字节未变**。
func TestRiskWecomSettingsControllerRedactsAndStaysIndependent(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	admin := &entity.User{ID: 1, Role: "admin"}

	// 先通过 HTTP 配工单（同 URL 的两条独立记录）。
	if _, err := service.UpdateTicketWecomSettingsWithDB(db, service.UpdateTicketWecomSettingsInput{
		Enabled: boolPtrForRiskTest(true), WebhookURL: riskControllerTestTicketWebhook,
	}); err != nil {
		t.Fatalf("seed ticket settings: %v", err)
	}
	resp := performRiskRequest(t, admin, http.MethodPut, "/risk/webhook",
		`{"enabled":true,"webhookUrl":"`+riskControllerTestRiskWebhook+`","mentionedList":["alice","alice","bob"]}`,
		nil, UpdateRiskWecomSettings)
	body := readRiskBody(t, resp)
	var parsed struct {
		Data service.RiskWecomSettings `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, body)
	}
	if parsed.Data.WebhookURL != "" {
		t.Fatalf("webhookUrl must never be echoed, got %q", parsed.Data.WebhookURL)
	}
	if parsed.Data.Preview != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置" {
		t.Fatalf("unexpected preview: %q", parsed.Data.Preview)
	}
	if !parsed.Data.Configured || !parsed.Data.Enabled || len(parsed.Data.MentionedList) != 2 {
		t.Fatalf("unexpected settings: %+v", parsed.Data)
	}
	// 凭据本身绝不能出现在响应体里（哪怕藏在别的字段）。
	if strings.Contains(body, "key=risk-ctrl") {
		t.Fatalf("response leaked the credential: %s", body)
	}
	// 读回：仍为空 URL。
	resp = performRiskRequest(t, admin, http.MethodGet, "/risk/webhook", "", nil, RiskWecomSettings)
	body = readRiskBody(t, resp)
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if resp.Code != http.StatusOK || parsed.Data.WebhookURL != "" || !parsed.Data.Configured {
		t.Fatalf("status=%d body=%s", resp.Code, body)
	}
	// 只改开关（空白 URL）：必须保持已配置。
	resp = performRiskRequest(t, admin, http.MethodPut, "/risk/webhook", `{"enabled":false}`, nil, UpdateRiskWecomSettings)
	if err := json.Unmarshal([]byte(readRiskBody(t, resp)), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Code != http.StatusOK || !parsed.Data.Configured || parsed.Data.Enabled {
		t.Fatalf("toggle-only update broke the stored webhook: %+v", parsed.Data)
	}
	// 清除风控：工单那条必须完好（96 §0 C3 判据 3）。
	resp = performRiskRequest(t, admin, http.MethodPut, "/risk/webhook", `{"clearWebhook":true}`, nil, UpdateRiskWecomSettings)
	if err := json.Unmarshal([]byte(readRiskBody(t, resp)), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Code != http.StatusOK || parsed.Data.Configured || parsed.Data.Preview != "" {
		t.Fatalf("clear did not take effect: %+v", parsed.Data)
	}
	ticketConfig, err := service.GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if ticketConfig.WebhookURL != riskControllerTestTicketWebhook {
		t.Fatalf("clearing the risk webhook must not touch the ticket webhook, got %q", ticketConfig.WebhookURL)
	}
}

// 非法地址必须 400，且错误文案指明是**风控**那个输入框（两个框并排）。
func TestRiskWecomSettingsControllerRejectsNonWecomURL(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	admin := &entity.User{ID: 1, Role: "admin"}
	resp := performRiskRequest(t, admin, http.MethodPut, "/risk/webhook",
		`{"enabled":true,"webhookUrl":"http://example.com/hook"}`, nil, UpdateRiskWecomSettings)
	body := readRiskBody(t, resp)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.Code, body)
	}
	if !strings.Contains(body, "风控") {
		t.Fatalf("error must name the risk input box: %s", body)
	}
	// 参数不是合法 JSON → 400。
	resp = performRiskRequest(t, admin, http.MethodPut, "/risk/webhook", `{`, nil, UpdateRiskWecomSettings)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.Code)
	}
}

// 测试发送：风控未配置时**直接 400**，绝不借工单通道（96 C2）。
//
// 三态都要覆盖：
//  1. 风控 URL 空 + 工单已配 → 400 且 sender 一次都没被调用；
//  2. 风控已配但未启用 → 400；
//  3. 风控已配且启用 → 200，且发送用的是**风控 URL**。
func TestTestRiskWecomWebhookNeverBorrowsTicketConfig(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	admin := &entity.User{ID: 1, Role: "admin"}

	previous := riskAdminWebhookSend
	t.Cleanup(func() { riskAdminWebhookSend = previous })
	var sent []service.RiskWecomConfig
	riskAdminWebhookSend = func(_ context.Context, config service.RiskWecomConfig, content string, _ []string) error {
		sent = append(sent, config)
		if !strings.Contains(content, "风控") {
			t.Fatalf("test message must say it is the risk channel: %s", content)
		}
		return nil
	}

	// 只配工单。
	if _, err := service.UpdateTicketWecomSettingsWithDB(db, service.UpdateTicketWecomSettingsInput{
		Enabled: boolPtrForRiskTest(true), WebhookURL: riskControllerTestTicketWebhook,
	}); err != nil {
		t.Fatalf("seed ticket settings: %v", err)
	}
	resp := performRiskRequest(t, admin, http.MethodPost, "/risk/webhook/test", "", nil, TestRiskWecomWebhook)
	body := readRiskBody(t, resp)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.Code, body)
	}
	if !strings.Contains(body, "风控") {
		t.Fatalf("message must point at the risk input box: %s", body)
	}
	if len(sent) != 0 {
		t.Fatalf("must not borrow the ticket webhook, sent=%v", sent)
	}

	// 风控已配但未启用。
	if _, err := service.UpdateRiskWecomSettings(db, service.UpdateRiskWecomSettingsInput{
		Enabled: boolPtrForRiskTest(false), WebhookURL: riskControllerTestRiskWebhook,
	}); err != nil {
		t.Fatalf("seed risk settings: %v", err)
	}
	resp = performRiskRequest(t, admin, http.MethodPost, "/risk/webhook/test", "", nil, TestRiskWecomWebhook)
	if resp.Code != http.StatusBadRequest || len(sent) != 0 {
		t.Fatalf("disabled risk notification must be 400, status=%d sent=%d", resp.Code, len(sent))
	}

	// 启用 + 已配 → 200 且使用风控 URL。
	if _, err := service.UpdateRiskWecomSettings(db, service.UpdateRiskWecomSettingsInput{
		Enabled: boolPtrForRiskTest(true), WebhookURL: riskControllerTestRiskWebhook,
	}); err != nil {
		t.Fatalf("seed risk settings: %v", err)
	}
	resp = performRiskRequest(t, admin, http.MethodPost, "/risk/webhook/test", "", nil, TestRiskWecomWebhook)
	if resp.Code != http.StatusOK || len(sent) != 1 {
		t.Fatalf("status=%d sent=%d body=%s", resp.Code, len(sent), readRiskBody(t, resp))
	}
	if sent[0].WebhookURL != riskControllerTestRiskWebhook {
		t.Fatalf("test send used %q, want the risk webhook %q", sent[0].WebhookURL, riskControllerTestRiskWebhook)
	}

	// 发信报错 → 502（不回 500：这是下游 4xx/5xx 类失败）。
	riskAdminWebhookSend = func(context.Context, service.RiskWecomConfig, string, []string) error {
		return errors.New("simulated outage")
	}
	resp = performRiskRequest(t, admin, http.MethodPost, "/risk/webhook/test", "", nil, TestRiskWecomWebhook)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
}

// 没有数据库时必须 503（而不是 panic）：单测/未初始化环境下不能崩。
//
// ⚠️ webhook 更新走的是 PUT + JSON 体：它**先绑参数再查库**（非法入参 400 优先于
// 基础设施 503，这是有意的快失败）。所以这里每个 handler 用各自合法的请求形态，
// 否则测的是「参数错误」而不是「没有数据库」——那是两个完全不同的分支。
func TestRiskControllersWithoutDatabaseReturn503(t *testing.T) {
	previous := riskAdminDB
	riskAdminDB = func() *gorm.DB { return nil }
	t.Cleanup(func() { riskAdminDB = previous })
	admin := &entity.User{ID: 1, Role: "admin"}
	cases := []struct {
		name    string
		method  string
		body    string
		params  gin.Params
		handler gin.HandlerFunc
	}{
		{name: "list", method: http.MethodGet, handler: RiskWarningList},
		{name: "detail", method: http.MethodGet, params: gin.Params{{Key: "id", Value: "1"}}, handler: RiskWarningDetail},
		{name: "webhook read", method: http.MethodGet, handler: RiskWecomSettings},
		{name: "webhook update", method: http.MethodPut, body: `{"enabled":false}`, handler: UpdateRiskWecomSettings},
		{name: "webhook send", method: http.MethodPost, handler: TestRiskWecomWebhook},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performRiskRequest(t, admin, tc.method, "/risk/alerts", tc.body, tc.params, tc.handler)
			if resp.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
			}
		})
	}
}

// 时间解析：纯日期推到当天 23:59:59.999999999（createdTo 含当天整天）。
func TestParseRiskTimeQueryEndOfDay(t *testing.T) {
	value, err := parseRiskTimeQuery("2026-11-13", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if value.Hour() == 0 && value.Minute() == 0 && value.Second() == 0 {
		t.Fatalf("createdTo must cover the whole day, got %v", value)
	}
	start, err := parseRiskTimeQuery("2026-11-13", false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if start.Day() != 13 || start.Hour() != 0 || start.Minute() != 0 {
		t.Fatalf("createdFrom must be midnight, got %v", start)
	}
	if _, err := parseRiskTimeQuery("not-a-time", false); err == nil {
		t.Fatal("invalid time must be an error")
	}
	if empty, err := parseRiskTimeQuery("   ", false); err != nil || empty != nil {
		t.Fatalf("blank must yield nil, got %v %v", empty, err)
	}
}

func boolPtrForRiskTest(value bool) *bool { return &value }
