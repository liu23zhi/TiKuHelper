package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 风控非数值配置（ASN 名单 / 评估间隔）的控制器用例。
//
// 这组 handler 的价值全在「**接线**」：service 层的读写已经被
// risk_config_settings_test.go 钉住，这里要证明的是
//   · 三条路由真的指向这三个 handler（不是又一批「实现完了但 router 没挂」）；
//   · 400 的文案由后端原文给出（运营要知道「哪一项、为什么」）；
//   · 缺字段与空串必须区分（一次手滑的请求不能静默关掉 R10）。

func newRiskConfigControllerDB(t *testing.T) {
	t.Helper()
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
}

// riskConfigAdminUser 是本文件专用的管理员身份。
//
// 不复用 user_exam_tool_test.go 里既有的 adminUser()：那是另一个用例集的
// 身份（id/username 与风控用例无关），跨用例共享一个可变 struct 会让
// 「某个用例改了它」变成隐蔽的串扰源。
func riskConfigAdminUser() *entity.User {
	return &entity.User{ID: 99, Username: "risk-config-admin", Role: "admin"}
}

// performRiskConfigRequest 走真实 handler（含 requireAdmin 的二次校验）。
func performRiskConfigRequest(t *testing.T, method, target, body string, params gin.Params, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	return performRiskRequest(t, riskConfigAdminUser(), method, target, body, params, handler)
}

// ① 三条 handler 都必须拒绝普通用户与匿名请求（纵深防御，与既有 5 条同纪律）。
func TestRiskConfigHandlersRequireAdmin(t *testing.T) {
	newRiskConfigControllerDB(t)
	handlers := map[string]gin.HandlerFunc{
		"config list":   RiskConfigList,
		"config update": UpdateRiskConfig,
		"config reset":  ResetRiskConfig,
	}
	params := gin.Params{{Key: "key", Value: service.RiskThresholdSettingKeyProxyASNList}}
	for name, handler := range handlers {
		t.Run(name+"/user_forbidden", func(t *testing.T) {
			resp := performRiskRequest(t, &entity.User{ID: 1, Role: "user"},
				http.MethodGet, "/risk/config", "", params, handler)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
			}
		})
		t.Run(name+"/anonymous_unauthorized", func(t *testing.T) {
			if code := performRiskRequestNoUser(t, "/risk/config", handler); code != http.StatusUnauthorized {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

// ② 读接口返回两项配置，且 Kind 足以让前端选控件。
func TestRiskConfigListController(t *testing.T) {
	newRiskConfigControllerDB(t)
	resp := performRiskConfigRequest(t, http.MethodGet, "/risk/config", "", nil, RiskConfigList)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	var body struct {
		Items []service.RiskConfigItem `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, resp.Body.String())
	}
	// 断言「返回全部已登记项」，而不是写死一个数字：
	// 写死数字会让**新增配置项**这件事本身把测试弄红（本轮加凌晨时段两项就踩到了），
	// 而测试真正要防的是「登记了却没返回」（漏项）。
	if len(body.Items) != len(service.RiskConfigKeysForTest()) {
		t.Fatalf("应返回全部 %d 项登记配置，实际 %d 项：%+v",
			len(service.RiskConfigKeysForTest()), len(body.Items), body.Items)
	}
	byKey := map[string]service.RiskConfigItem{}
	for _, item := range body.Items {
		byKey[item.Key] = item
	}
	// ASN 名单：kind 必须是 asn_list（前端据此渲染文本域而不是数字框）。
	asn, ok := byKey[service.RiskThresholdSettingKeyProxyASNList]
	if !ok {
		t.Fatalf("缺少 ASN 名单项，实际键：%v", service.SortedRiskConfigKeys())
	}
	if asn.Kind != "asn_list" {
		t.Fatalf("ASN 名单的 kind = %q, want %q", asn.Kind, "asn_list")
	}
	if asn.DefaultValue != "" {
		t.Fatalf("ASN 名单默认值必须是空串（空 = 关闭 R10），实际 %q", asn.DefaultValue)
	}
	// Help 必须写明「留空意味着什么」—— 这是运营判断「规则为什么不响」的唯一来源。
	if !strings.Contains(asn.Help, "留空") && !strings.Contains(asn.Help, "为空") {
		t.Fatalf("ASN 名单的 Help 必须说明留空的语义，实际 %q", asn.Help)
	}
	// 评估间隔：kind + 上下界齐全（前端据此做 min/max）。
	interval := byKey[service.RiskEvaluateIntervalSettingKey]
	if interval.Kind != "positive_int" {
		t.Fatalf("评估间隔的 kind = %q, want %q", interval.Kind, "positive_int")
	}
	if interval.MinValue != 1 || interval.MaxValue != 1440 {
		t.Fatalf("评估间隔区间 = [%v, %v], want [1, 1440]", interval.MinValue, interval.MaxValue)
	}
	if interval.Unit != "分钟" {
		t.Fatalf("评估间隔单位 = %q, want 分钟", interval.Unit)
	}
}

// ③ 写入成功路径 + 非法值 400 且文案含标签（运营要知道是哪一项）。
func TestRiskConfigUpdateController(t *testing.T) {
	newRiskConfigControllerDB(t)
	params := gin.Params{{Key: "key", Value: service.RiskThresholdSettingKeyProxyASNList}}

	resp := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{"value":"AS15169, AS16509"}`, params, UpdateRiskConfig)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	var ok struct {
		Message string                 `json:"message"`
		Item    service.RiskConfigItem `json:"item"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 字面量断言（不引用常量拼期望串）。
	if ok.Item.Value != "AS15169,AS16509" {
		t.Fatalf("落库值 = %q, want %q", ok.Item.Value, "AS15169,AS16509")
	}
	if !ok.Item.Configured {
		t.Fatalf("保存后 Configured 必须为 true")
	}

	// 非法值：400 + 原文可见。
	bad := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{"value":"AS15169,not-an-asn"}`, params, UpdateRiskConfig)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("坏 ASN 必须 400，实际 status=%d body=%s", bad.Code, readRiskBody(t, bad))
	}
	var badBody struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(bad.Body.Bytes(), &badBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(badBody.Message, "ASN 名单") {
		t.Fatalf("400 文案必须指明是哪一项（实际 %q）：只说「取值非法」运营无从自查", badBody.Message)
	}
	if !strings.Contains(badBody.Message, "AS15169") {
		t.Fatalf("400 文案应给出合法形态示例（实际 %q）", badBody.Message)
	}
}

// ④ 缺字段与空串必须区分 —— 这是本组 handler 最容易做错的一处。
//
// 空串对 ASN 名单是合法值（= 关闭 R10），对间隔是重置语义。
// 若用裸 string，缺字段会被静默当成「清空」：一次手滑的请求就能关掉规则。
func TestRiskConfigUpdateDistinguishesMissingFromEmpty(t *testing.T) {
	newRiskConfigControllerDB(t)
	params := gin.Params{{Key: "key", Value: service.RiskThresholdSettingKeyProxyASNList}}
	// 先配上一个名单，才能证明「缺字段没有清空它」。
	if resp := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{"value":"AS15169"}`, params, UpdateRiskConfig); resp.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}

	// 缺 value 字段 → 400（不能当成空串）。
	missing := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{}`, params, UpdateRiskConfig)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("缺 value 必须 400（否则手滑请求会静默清空配置），实际 status=%d body=%s",
			missing.Code, readRiskBody(t, missing))
	}
	// 且名单没变。
	list := performRiskConfigRequest(t, http.MethodGet, "/risk/config", "", nil, RiskConfigList)
	var body struct {
		Items []service.RiskConfigItem `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range body.Items {
		if item.Key == service.RiskThresholdSettingKeyProxyASNList && item.Value != "AS15169" {
			t.Fatalf("缺字段请求不得改动配置，实际 %q", item.Value)
		}
	}

	// 显式空串 → 200 且重置（区别于上面的 400）。
	explicit := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{"value":""}`, params, UpdateRiskConfig)
	if explicit.Code != http.StatusOK {
		t.Fatalf("显式空串必须 200（= 关闭 R10），实际 status=%d body=%s",
			explicit.Code, readRiskBody(t, explicit))
	}
	var explicitBody struct {
		Message string                 `json:"message"`
		Item    service.RiskConfigItem `json:"item"`
	}
	if err := json.Unmarshal(explicit.Body.Bytes(), &explicitBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if explicitBody.Item.Configured {
		t.Fatalf("显式空串后 Configured 必须为 false")
	}
	// 文案必须说清「变成了什么」，不能只说「已保存」。
	if !strings.Contains(explicitBody.Message, "默认") && !strings.Contains(explicitBody.Message, "清空") {
		t.Fatalf("清空成功的文案必须说明回到默认值，实际 %q", explicitBody.Message)
	}
}

// ⑤ 重置接口：删行回到默认值。
func TestRiskConfigResetController(t *testing.T) {
	newRiskConfigControllerDB(t)
	params := gin.Params{{Key: "key", Value: service.RiskEvaluateIntervalSettingKey}}
	if resp := performRiskConfigRequest(t, http.MethodPut, "/risk/config/"+params[0].Value,
		`{"value":"60"}`, params, UpdateRiskConfig); resp.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	resp := performRiskConfigRequest(t, http.MethodDelete, "/risk/config/"+params[0].Value, "", params, ResetRiskConfig)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, readRiskBody(t, resp))
	}
	var body struct {
		Item service.RiskConfigItem `json:"item"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 字面量断言默认值 10（不引用常量，常量漂移时必红）。
	if body.Item.Value != "10" {
		t.Fatalf("重置后生效值 = %q, want %q", body.Item.Value, "10")
	}
	if body.Item.Configured {
		t.Fatalf("重置后 Configured 必须为 false")
	}

	// 未知键：400（不是 500）且不写库。
	unknown := gin.Params{{Key: "key", Value: "risk_rule_debt_user_threshold"}}
	bad := performRiskConfigRequest(t, http.MethodDelete, "/risk/config/"+unknown[0].Value, "", unknown, ResetRiskConfig)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("未知键必须 400（不得写任意 system_settings 键），实际 status=%d body=%s",
			bad.Code, readRiskBody(t, bad))
	}
}

// ⑥ **路由接线断言（源码级）**：三条路由必须存在且挂满中间件。
//
// 为什么必须读源码：handler 内部各有一道 requireAdmin（纵深防御），
// 所以把 router.go 里这三条 middleware.RequireAdmin 删掉，只发请求的行为
// 测试**依然全绿** —— 测试用的是自己拼的 handler，中间件链不是生产那一份。
// 同一纪律见 internal/api/script_activation_code_routes_test.go。
func TestRiskConfigRoutesMountedInRouterSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "api", "router.go"))
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	lines := strings.Split(string(source), "\n")

	cases := []struct {
		method string
		path   string
		handle string
	}{
		{"GET", "/risk/config", "controller.RiskConfigList"},
		{"PUT", "/risk/config/:key", "controller.UpdateRiskConfig"},
		{"DELETE", "/risk/config/:key", "controller.ResetRiskConfig"},
	}
	for _, testCase := range cases {
		marker := `"` + testCase.path + `"`
		var matched []string
		for _, line := range lines {
			if !strings.Contains(line, "apiRouter.") {
				continue
			}
			if strings.Contains(line, testCase.method+"(") && strings.Contains(line, marker) {
				matched = append(matched, line)
			}
		}
		if len(matched) != 1 {
			t.Fatalf("%s %s 应恰好注册 1 条，实际 %d 条：%v",
				testCase.method, testCase.path, len(matched), matched)
		}
		line := matched[0]
		if !strings.Contains(line, "controller.Auth") {
			t.Fatalf("%s %s 未挂 controller.Auth：%s", testCase.method, testCase.path, strings.TrimSpace(line))
		}
		requireAdminAt := strings.Index(line, "middleware.RequireAdmin")
		if requireAdminAt < 0 {
			t.Fatalf("%s %s 未挂 middleware.RequireAdmin：%s", testCase.method, testCase.path, strings.TrimSpace(line))
		}
		handlerAt := strings.Index(line, testCase.handle)
		if handlerAt < 0 {
			t.Fatalf("%s %s 未指向 %s：%s", testCase.method, testCase.path, testCase.handle, strings.TrimSpace(line))
		}
		if requireAdminAt > handlerAt {
			t.Fatalf("%s %s 的 RequireAdmin 排在 handler 之后，不会生效：%s",
				testCase.method, testCase.path, strings.TrimSpace(line))
		}
		if strings.Index(line, "controller.Auth") > requireAdminAt {
			t.Fatalf("%s %s 的 controller.Auth 排在 RequireAdmin 之后，RequireAdmin 拿不到身份：%s",
				testCase.method, testCase.path, strings.TrimSpace(line))
		}
	}
}
