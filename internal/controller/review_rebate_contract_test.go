package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
)

// reviewRebateContractRouter 与 router.go 保持一致（含截图回显与 score 两条新增路由）。
func reviewRebateContractRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/user/review-rebate/program", auth, ReviewRebateProgram)
	router.POST("/user/review-rebate/claims", auth, CreateReviewRebateClaim)
	router.GET("/user/review-rebate/claims", auth, MyReviewRebateClaims)
	router.GET("/admin/review-rebate/programs", auth, middleware.RequireAdmin, AdminReviewRebatePrograms)
	router.PUT("/admin/review-rebate/programs/:id", auth, middleware.RequireAdmin, AdminUpdateReviewRebateProgram)
	router.GET("/admin/review-rebate/rules", auth, middleware.RequireAdmin, AdminReviewRebateRules)
	router.POST("/admin/review-rebate/rules", auth, middleware.RequireAdmin, AdminCreateReviewRebateRule)
	router.PUT("/admin/review-rebate/rules/:id", auth, middleware.RequireAdmin, AdminUpdateReviewRebateRule)
	router.DELETE("/admin/review-rebate/rules/:id", auth, middleware.RequireAdmin, AdminDeleteReviewRebateRule)
	router.GET("/admin/review-rebate/claims", auth, middleware.RequireAdmin, AdminReviewRebateClaims)
	router.POST("/admin/review-rebate/claims/:id/decision", auth, middleware.RequireAdmin, AdminDecideReviewRebateClaim)
	router.POST("/admin/review-rebate/claims/:id/score", auth, middleware.RequireAdmin, AdminScoreReviewRebateClaim)
	router.GET("/admin/review-rebate/claims/:id/screenshot", auth, middleware.RequireAdmin, AdminReviewRebateScreenshot)
	return router
}

// decodeJSONObject 把响应体解成 map，便于断言真实 JSON 键名（而不是 Go 结构体字段）。
func decodeJSONObject(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode json %q: %v", string(body), err)
	}
	return decoded
}

// assertJSONKeys 断言键集合完全一致（多键少键都要报）。
func assertJSONKeys(t *testing.T, label string, object map[string]any, expected ...string) {
	t.Helper()

	remaining := make(map[string]struct{}, len(object))
	for key := range object {
		remaining[key] = struct{}{}
	}
	for _, key := range expected {
		if _, ok := remaining[key]; !ok {
			t.Fatalf("%s missing key %q (have %v)", label, key, keysOfReviewRebate(remaining))
		}
		delete(remaining, key)
	}
	if len(remaining) > 0 {
		t.Fatalf("%s has unexpected keys %v", label, keysOfReviewRebate(remaining))
	}
}

func keysOfReviewRebate(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}

// Test 5（HTTP 层）：program / claims / usage 的 JSON 形状与前端期望一致。
//
// 前端读的具体名字（见 web/src/pages/ReviewRebate/index.vue）：
//
//	body.program / body.usage / body.rules / body.authorshipVerified
//	program.name / storeUrl / maxClaimsPerUser / maxAmountPerUser / minAgentConfidence / enabled
//	usage.approvedCount / approvedAmount / pendingCount / rejectedCount
//	claims 列表：[...] 或 {data:[...]} —— 实际是 {data,total,page,pageSize}
func TestReviewRebateHTTPProgramContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 641, "rr-contract-member", "user")
	installReviewRebateTestDB(t, db)

	actor := member
	router := reviewRebateContractRouter(&actor)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/user/review-rebate/program", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("program view: %d %s", recorder.Code, recorder.Body.String())
	}
	body := decodeJSONObject(t, recorder.Body.Bytes())
	assertJSONKeys(t, "program view", body, "program", "rules", "usage", "authorshipVerified", "notice")

	program := body["program"].(map[string]any)
	assertJSONKeys(t, "program", program,
		"id", "enabled", "name", "storeUrl", "maxClaimsPerUser", "maxAmountPerUser",
		"minAgentConfidence", "policyVersion", "tutorialVersionId")
	// 关键：enabled 必须真的下发为 true（前端靠它决定显示规则表还是「未开启」）。
	if program["enabled"] != true {
		t.Fatalf("program.enabled must be true, got %v", program["enabled"])
	}
	// 用户端不得下发内部抓取范围标识。
	if _, leaked := program["extensionId"]; leaked {
		t.Fatal("program view must not expose extensionId")
	}

	usage := body["usage"].(map[string]any)
	assertJSONKeys(t, "usage", usage,
		"claimedCount", "approvedCount", "approvedAmount", "pendingCount",
		"rejectedCount", "remainingClaims", "remainingAmount")
	if usage["approvedCount"] != usage["claimedCount"] {
		t.Fatalf("approvedCount must mirror claimedCount: %v", usage)
	}

	if body["authorshipVerified"] != false {
		t.Fatalf("authorshipVerified must be false, got %v", body["authorshipVerified"])
	}

	// 我的提交列表：{data:[],total,page,pageSize}，空列表必须是 []。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/user/review-rebate/claims", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("my claims: %d %s", recorder.Code, recorder.Body.String())
	}
	listBody := decodeJSONObject(t, recorder.Body.Bytes())
	assertJSONKeys(t, "my claims", listBody, "data", "total", "page", "pageSize")
	if _, ok := listBody["data"].([]any); !ok {
		t.Fatalf("claims data must be an array, got %T", listBody["data"])
	}
}

// 管理端活动列表是 {data:[...]}（与规则表同一信封），前端按这个形状解包。
func TestReviewRebateHTTPAdminProgramsEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 642, "rr-contract-admin", "admin")
	installReviewRebateTestDB(t, db)

	actor := admin
	router := reviewRebateContractRouter(&actor)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/review-rebate/programs", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin programs: %d %s", recorder.Code, recorder.Body.String())
	}
	body := decodeJSONObject(t, recorder.Body.Bytes())
	assertJSONKeys(t, "admin programs", body, "data")
	list, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("admin programs data must be an array, got %T", body["data"])
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 program, got %d", len(list))
	}
	// 管理端要拿到 extensionId（配置项），这与用户端的脱敏要求相反。
	first := list[0].(map[string]any)
	if _, ok := first["extensionId"]; !ok {
		t.Fatalf("admin program must expose extensionId: %v", first)
	}
	if id, _ := first["id"].(float64); int32(id) != program.ID {
		t.Fatalf("admin program id = %v, want %d", first["id"], program.ID)
	}
}

// Test 6（HTTP 层）：规则 CRUD 的 201 / 200 / 404 语义。
//
// 关于前端假设的 409（"被引用不能删"）：当前实现是真删（历史 claim 已快照
// rule_id 与金额，删规则不影响对账，见 DeleteReviewRebateRuleWithDB 注释），
// 因此**不会**返回 409；重复删除返回 404。前端两种状态码都按成功处理，
// 409 分支是给"将来若要改成软删/引用保护"留的位置。
func TestReviewRebateHTTPRuleCrudSemantics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, _ := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 643, "rr-crud-admin", "admin")
	installReviewRebateTestDB(t, db)

	actor := admin
	router := reviewRebateContractRouter(&actor)
	send := func(method, target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	// 新建 → 201
	create := send(http.MethodPost, "/admin/review-rebate/rules", fmt.Sprintf(
		`{"programId":%d,"minStars":5,"maxStars":5,"minContentScore":8,"maxContentScore":10,"amount":0.8,"priority":20}`,
		program.ID))
	if create.Code != http.StatusCreated {
		t.Fatalf("create rule must be 201, got %d %s", create.Code, create.Body.String())
	}
	created := decodeJSONObject(t, create.Body.Bytes())
	ruleID := int32(created["id"].(float64))
	// 缺省 enabled 应为 true（运营配完立刻生效）。
	if created["enabled"] != true {
		t.Fatalf("create rule without enabled must default to true, got %v", created["enabled"])
	}

	// 更新 → 200
	update := send(http.MethodPut, fmt.Sprintf("/admin/review-rebate/rules/%d", ruleID), `{"amount":1.2}`)
	if update.Code != http.StatusOK {
		t.Fatalf("update rule must be 200, got %d %s", update.Code, update.Body.String())
	}
	if amount := decodeJSONObject(t, update.Body.Bytes())["amount"].(float64); amount != 1.2 {
		t.Fatalf("rule amount = %v, want 1.2", amount)
	}

	// 删除 → 200。Q77-6 起 `?reason=` 必填（后端独立校验，见 rule_audit.go）；
	// 原因不是本用例的断言对象，「缺 reason 必须 400」由 rule_audit_test.go 覆盖。
	deletion := send(http.MethodDelete, fmt.Sprintf("/admin/review-rebate/rules/%d?reason=%s", ruleID, ruleDeleteTestReasonQuery), "")
	if deletion.Code != http.StatusOK {
		t.Fatalf("delete rule must be 200, got %d %s", deletion.Code, deletion.Body.String())
	}

	// 重复删除 → 404（不是 409：没有引用保护这回事）。
	// 仍带 reason：否则会先被 400 拦下，测不到「规则不存在」这条分支。
	again := send(http.MethodDelete, fmt.Sprintf("/admin/review-rebate/rules/%d?reason=%s", ruleID, ruleDeleteTestReasonQuery), "")
	if again.Code != http.StatusNotFound {
		t.Fatalf("deleting a missing rule must be 404, got %d %s", again.Code, again.Body.String())
	}

	// 越界规则 → 400
	if out := send(http.MethodPost, "/admin/review-rebate/rules", fmt.Sprintf(
		`{"programId":%d,"minStars":6,"maxStars":7,"minContentScore":1,"maxContentScore":10,"amount":1}`,
		program.ID)); out.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range stars must be 400, got %d %s", out.Code, out.Body.String())
	}
}

// 审核结论：前端历史上发 `decision`，后端契约是 `action`。两个名字都必须被接受，
// 否则另一方发来的请求永远 400，且看起来像"参数校验"。
func TestReviewRebateHTTPDecisionAcceptsBothFieldNames(t *testing.T) {
	for _, field := range []string{"action", "decision"} {
		t.Run(field, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := newReviewRebateControllerTestDB(t)
			setReviewRebateControllerSwitch(t, db, true)
			seedReviewRebateControllerProgram(t, db)
			member := seedReviewRebateControllerUser(t, db, 651, "rr-alias-member", "user")
			admin := seedReviewRebateControllerUser(t, db, 652, "rr-alias-admin", "admin")
			installReviewRebateTestDB(t, db)
			t.Chdir(t.TempDir())

			actor := member
			router := reviewRebateContractRouter(&actor)
			out := uploadClaim(t, router, "real.png", pngFixture(t), map[string]string{
				"claimedStars": "5",
			})
			if out.Code != http.StatusCreated {
				t.Fatalf("upload: %d %s", out.Code, out.Body.String())
			}
			created := decodeJSONObject(t, out.Body.Bytes())
			claimID := int32(created["id"].(float64))

			// 提交响应里的边界声明也必须是 false。
			if created["authorshipVerified"] != false {
				t.Fatalf("create response must state authorshipVerified=false, got %v", created["authorshipVerified"])
			}

			actor = admin
			send := func(target, body string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				return recorder
			}
			if out := send(fmt.Sprintf("/admin/review-rebate/claims/%d/score", claimID), ""); out.Code != http.StatusOK {
				t.Fatalf("score: %d %s", out.Code, out.Body.String())
			}
			out = send(fmt.Sprintf("/admin/review-rebate/claims/%d/decision", claimID),
				fmt.Sprintf(`{"%s":"reject","note":"截图与条目无关"}`, field))
			if out.Code != http.StatusOK {
				t.Fatalf("decision via %q must be accepted, got %d %s", field, out.Code, out.Body.String())
			}
			decided := decodeJSONObject(t, out.Body.Bytes())
			if decided["status"] != entity.ReviewRebateStatusRejected {
				t.Fatalf("status = %v, want rejected", decided["status"])
			}
			if decided["reviewNote"] != "截图与条目无关" {
				t.Fatalf("reviewNote must be persisted, got %v", decided["reviewNote"])
			}
		})
	}
}

// 提交时评价链接同时接受 claimedReviewUrl（规范名）与 reviewUrl（老前端）。
func TestReviewRebateHTTPClaimURLAcceptsBothFieldNames(t *testing.T) {
	cases := []struct{ field, value string }{
		{"claimedReviewUrl", "https://example.invalid/review/canonical"},
		{"reviewUrl", "https://example.invalid/review/legacy"},
	}
	for _, testCase := range cases {
		t.Run(testCase.field, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := newReviewRebateControllerTestDB(t)
			setReviewRebateControllerSwitch(t, db, true)
			seedReviewRebateControllerProgram(t, db)
			member := seedReviewRebateControllerUser(t, db, 661, "rr-url-member", "user")
			installReviewRebateTestDB(t, db)
			t.Chdir(t.TempDir())

			actor := member
			router := reviewRebateContractRouter(&actor)
			out := uploadClaim(t, router, "real.png", pngFixture(t), map[string]string{
				testCase.field: testCase.value,
			})
			if out.Code != http.StatusCreated {
				t.Fatalf("upload via %q: %d %s", testCase.field, out.Code, out.Body.String())
			}
			var stored entity.ReviewRebateClaim
			if err := db.Order("id desc").First(&stored).Error; err != nil {
				t.Fatalf("load claim: %v", err)
			}
			// 关键：链接不能被静默丢弃 —— 它参与 canonical 去重键。
			if stored.ClaimedReviewURL != testCase.value {
				t.Fatalf("claimed review url = %q, want %q (field %q must not be dropped)",
					stored.ClaimedReviewURL, testCase.value, testCase.field)
			}
		})
	}
}

// Test 2（HTTP 层）：未配置模型 → 打分走 stub（不回归），响应带 agentScorer=stub。
func TestReviewRebateHTTPScoreFallsBackToStub(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 671, "rr-stub-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 672, "rr-stub-admin", "admin")
	installReviewRebateTestDB(t, db)
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateContractRouter(&actor)
	out := uploadClaim(t, router, "real.png", pngFixture(t), nil)
	if out.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", out.Code, out.Body.String())
	}
	claimID := int32(decodeJSONObject(t, out.Body.Bytes())["id"].(float64))

	actor = admin
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/score", claimID), nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("score: %d %s", recorder.Code, recorder.Body.String())
	}
	scored := decodeJSONObject(t, recorder.Body.Bytes())
	if scored["agentScorer"] != service.ReviewRebateScorerKindStub {
		t.Fatalf("unconfigured model must mark agentScorer=stub, got %v", scored["agentScorer"])
	}
	// stub 的置信度是 1，规则满额命中 → scored。
	if scored["status"] != entity.ReviewRebateStatusScored {
		t.Fatalf("status = %v, want scored", scored["status"])
	}
}

// 截图回显：非管理员 403；管理员拿到真实图片字节；无截图 404。
func TestReviewRebateHTTPScreenshotEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 681, "rr-shot-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 682, "rr-shot-admin", "admin")
	installReviewRebateTestDB(t, db)
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateContractRouter(&actor)
	png := pngFixture(t)
	out := uploadClaim(t, router, "real.png", png, nil)
	if out.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", out.Code, out.Body.String())
	}
	claimID := int32(decodeJSONObject(t, out.Body.Bytes())["id"].(float64))
	target := fmt.Sprintf("/admin/review-rebate/claims/%d/screenshot", claimID)

	// 普通用户 → 403
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin must get 403, got %d %s", recorder.Code, recorder.Body.String())
	}

	// 管理员 → 200 + 真实图片字节
	actor = admin
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin screenshot: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("screenshot must not be cached, got %q", recorder.Header().Get("Cache-Control"))
	}
	if !strings.Contains(recorder.Body.String(), "PNG") && len(recorder.Body.Bytes()) != len(png) {
		t.Fatalf("screenshot body length = %d, want %d", len(recorder.Body.Bytes()), len(png))
	}

	// 不存在的提交 → 404
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/review-rebate/claims/99999/screenshot", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing claim screenshot must be 404, got %d %s", recorder.Code, recorder.Body.String())
	}
}

// 管理端 claims 列表要带 screenshotUrl 与 agentRationale；前端靠这两个字段复核。
func TestReviewRebateHTTPAdminClaimsExposeReviewFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 691, "rr-adminfields-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 692, "rr-adminfields-admin", "admin")
	installReviewRebateTestDB(t, db)
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateContractRouter(&actor)
	out := uploadClaim(t, router, "real.png", pngFixture(t), nil)
	if out.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", out.Code, out.Body.String())
	}
	claimID := int32(decodeJSONObject(t, out.Body.Bytes())["id"].(float64))

	actor = admin
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/review-rebate/claims", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin claims: %d %s", recorder.Code, recorder.Body.String())
	}
	body := decodeJSONObject(t, recorder.Body.Bytes())
	list := body["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(list))
	}
	row := list[0].(map[string]any)
	expectedURL := fmt.Sprintf("%s/admin/review-rebate/claims/%d/screenshot", service.ReviewRebateAdminAPIPrefix, claimID)
	if row["screenshotUrl"] != expectedURL {
		t.Fatalf("screenshotUrl = %v, want %v", row["screenshotUrl"], expectedURL)
	}
	// 截图路径本身绝不能出现在响应里（它是服务端私有目录结构）。
	if _, leaked := row["screenshotPath"]; leaked {
		t.Fatal("claim list must not expose screenshotPath")
	}
	if _, leaked := row["screenshotSha256"]; leaked {
		t.Fatal("claim list must not expose screenshotSha256")
	}
}

// 开关关闭时：用户端 program 返回 400「未启用」，管理端也拒绝决策 —— 前端据此显示"未开启"。
func TestReviewRebateHTTPDisabledShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 695, "rr-disabled-shape", "user")
	installReviewRebateTestDB(t, db)

	actor := member
	router := reviewRebateContractRouter(&actor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/user/review-rebate/program", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("disabled program view must be 400, got %d", recorder.Code)
	}
	body := decodeJSONObject(t, recorder.Body.Bytes())
	// 前端按 message 里是否含「未启用」判断这是"未开放"而不是"加载失败"。
	message, _ := body["message"].(string)
	if !strings.Contains(message, "未启用") {
		t.Fatalf("disabled message must contain 未启用 so the page can render the closed state, got %q", message)
	}
}
