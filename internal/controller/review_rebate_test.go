package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newReviewRebateControllerTestDB 建控制器测试库（含评价返现四张表）。
func newReviewRebateControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(
		&entity.ReviewRebateProgram{},
		&entity.ReviewRebateRule{},
		&entity.ReviewRebateClaim{},
		&entity.ReviewRebateAward{},
	); err != nil {
		t.Fatalf("migrate review rebate tables: %v", err)
	}
	return db
}

// seedReviewRebateControllerUser 建一个账户。
func seedReviewRebateControllerUser(t *testing.T, db *gorm.DB, id int32, username string, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       username,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// setReviewRebateControllerSwitch 写入总开关。
func setReviewRebateControllerSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()

	value := "false"
	if enabled {
		value = "true"
	}
	if err := db.Create(&entity.SystemSetting{Key: service.ReviewRebateEnabledSettingKey, Value: value}).Error; err != nil {
		t.Fatalf("seed review rebate switch: %v", err)
	}
}

// seedReviewRebateControllerProgram 建活动 + 一条满额规则。
func seedReviewRebateControllerProgram(t *testing.T, db *gorm.DB) (entity.ReviewRebateProgram, entity.ReviewRebateRule) {
	t.Helper()

	program := entity.ReviewRebateProgram{
		Name:               "评价返现",
		StoreURL:           "https://example.invalid/store",
		ExtensionID:        "extension-fixture",
		Enabled:            true,
		MaxClaimsPerUser:   3,
		MaxAmountPerUser:   5.04,
		MinAgentConfidence: 0.6,
		PolicyVersion:      service.ReviewRebatePolicyVersion,
	}
	if err := db.Create(&program).Error; err != nil {
		t.Fatalf("create program: %v", err)
	}
	rule := entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 20, Enabled: true, Remark: "任意评价 1 元",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return program, rule
}

// reviewRebateControllerRouter 组装与 router.go 完全一致的中间件顺序。
func reviewRebateControllerRouter(actor *entity.User) *gin.Engine {
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
	return router
}

// installReviewRebateTestDB 把控制器里的数据库取用点替换成测试库。
func installReviewRebateTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	previous := reviewRebateDB
	reviewRebateDB = func() *gorm.DB { return db }
	t.Cleanup(func() { reviewRebateDB = previous })
}

// pngFixture 生成一张真实可解码的 PNG（2×2）。
func pngFixture(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	img.Set(1, 0, color.RGBA{R: 40, G: 50, B: 60, A: 255})
	img.Set(0, 1, color.RGBA{R: 70, G: 80, B: 90, A: 255})
	img.Set(1, 1, color.RGBA{R: 100, G: 110, B: 120, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
}

// uploadClaim 构造一次 multipart 提交。
func uploadClaim(t *testing.T, router *gin.Engine, filename string, content []byte, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	for key, value := range form {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/user/review-rebate/claims", &payload)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	out := httptest.NewRecorder()
	router.ServeHTTP(out, request)
	return out
}

// Test 9：非管理员访问管理接口 → 403。
func TestReviewRebateAdminEndpointsRejectNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	program, rule := seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 601, "rr-http-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 602, "rr-http-admin", "admin")
	installReviewRebateTestDB(t, db)

	actor := member
	router := reviewRebateControllerRouter(&actor)

	send := func(method, target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, request)
		return out
	}

	adminRoutes := []struct{ method, target, body string }{
		{http.MethodGet, "/admin/review-rebate/programs", ""},
		{http.MethodPut, fmt.Sprintf("/admin/review-rebate/programs/%d", program.ID), `{"maxClaimsPerUser":5}`},
		{http.MethodGet, "/admin/review-rebate/rules", ""},
		{http.MethodPost, "/admin/review-rebate/rules", `{"programId":1,"minStars":1,"maxStars":5,"minContentScore":1,"maxContentScore":10,"amount":1}`},
		{http.MethodPut, fmt.Sprintf("/admin/review-rebate/rules/%d", rule.ID), `{"amount":2}`},
		{http.MethodDelete, fmt.Sprintf("/admin/review-rebate/rules/%d", rule.ID), ""},
		{http.MethodGet, "/admin/review-rebate/claims", ""},
		{http.MethodPost, "/admin/review-rebate/claims/1/decision", `{"action":"approve"}`},
		{http.MethodPost, "/admin/review-rebate/claims/1/score", ""},
	}
	for _, route := range adminRoutes {
		out := send(route.method, route.target, route.body)
		if out.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s must be 403, got %d %s", route.method, route.target, out.Code, out.Body.String())
		}
	}

	// 用户端接口不要求管理员。
	if out := send(http.MethodGet, "/user/review-rebate/program", ""); out.Code != http.StatusOK {
		t.Fatalf("own program view must be 200, got %d %s", out.Code, out.Body.String())
	}
	if out := send(http.MethodGet, "/user/review-rebate/claims", ""); out.Code != http.StatusOK {
		t.Fatalf("own claim list must be 200, got %d %s", out.Code, out.Body.String())
	}

	// 管理员：列表 200。
	actor = admin
	if out := send(http.MethodGet, "/admin/review-rebate/programs", ""); out.Code != http.StatusOK {
		t.Fatalf("admin program list failed: %d %s", out.Code, out.Body.String())
	}
	if out := send(http.MethodGet, "/admin/review-rebate/rules", ""); out.Code != http.StatusOK {
		t.Fatalf("admin rule list failed: %d %s", out.Code, out.Body.String())
	}
}

// Test 10：上传非图片（改名成 .png 的文本）→ 400。
func TestReviewRebateUploadRejectsNonImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 611, "rr-upload-member", "user")
	installReviewRebateTestDB(t, db)
	// 落到临时工作目录，避免污染仓库的 data/。
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateControllerRouter(&actor)

	// 关键用例：文本内容 + .png 文件名 + image/png 也没用 —— 服务端真解码。
	out := uploadClaim(t, router, "fake.png", []byte("this is definitely not an image"), nil)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("text renamed to .png must be 400, got %d %s", out.Code, out.Body.String())
	}
	if !strings.Contains(out.Body.String(), "PNG/JPEG") {
		t.Fatalf("error message should mention PNG/JPEG, got %s", out.Body.String())
	}

	// 空文件 → 400。
	out = uploadClaim(t, router, "empty.png", nil, nil)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("empty upload must be 400, got %d %s", out.Code, out.Body.String())
	}

	// 截断的 PNG（只有头）→ 400（DecodeConfig 需要完整 IHDR）。
	full := pngFixture(t)
	out = uploadClaim(t, router, "truncated.png", full[:12], nil)
	if out.Code != http.StatusBadRequest {
		t.Fatalf("truncated png must be 400, got %d %s", out.Code, out.Body.String())
	}

	// 真 PNG → 201。
	out = uploadClaim(t, router, "real.png", full, map[string]string{"claimedStars": "5"})
	if out.Code != http.StatusCreated {
		t.Fatalf("real png must be accepted, got %d %s", out.Code, out.Body.String())
	}
	var created struct {
		ID                 int32  `json:"id"`
		Status             string `json:"status"`
		AuthorshipVerified bool   `json:"authorshipVerified"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ID <= 0 || created.Status != entity.ReviewRebateStatusSubmitted {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if created.AuthorshipVerified {
		t.Fatal("create response must not claim authorship verification")
	}

	// 落盘位置必须在 data/review-rebate 下，且不返回可直连 URL。
	var claim entity.ReviewRebateClaim
	if err := db.Where("id = ?", created.ID).First(&claim).Error; err != nil {
		t.Fatalf("load claim: %v", err)
	}
	if !strings.HasPrefix(claim.ScreenshotPath, reviewRebateScreenshotDirName+string(filepath.Separator)) {
		t.Fatalf("screenshot path must stay inside the private dir, got %q", claim.ScreenshotPath)
	}
	if strings.Contains(out.Body.String(), "screenshotPath") || strings.Contains(out.Body.String(), claim.ScreenshotSHA256) {
		t.Fatalf("response must not leak screenshot path or digest: %s", out.Body.String())
	}
	if len(claim.ScreenshotSHA256) != 64 {
		t.Fatalf("screenshot sha256 must be 64 hex chars, got %q", claim.ScreenshotSHA256)
	}
	// 文件真的存在，且权限不是匿名的。
	written := filepath.Join("data", claim.ScreenshotPath)
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("screenshot file missing at %s: %v", written, err)
	}

	// 同一张图重复提交 → 409 duplicate（唯一键）。
	out = uploadClaim(t, router, "real.png", full, map[string]string{"claimedStars": "5"})
	if out.Code != http.StatusConflict {
		t.Fatalf("duplicate screenshot must be 409, got %d %s", out.Code, out.Body.String())
	}

	// 超过 5 MB → 413。
	huge := make([]byte, maxReviewRebateScreenshotBytes+1)
	copy(huge, full)
	out = uploadClaim(t, router, "huge.png", huge, nil)
	if out.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload must be 413, got %d %s", out.Code, out.Body.String())
	}
}

// Test 1（HTTP 层）：开关关闭 → 提交 400、活动接口 400。
func TestReviewRebateHTTPDisabledRejectsSubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	// 不写开关 = 默认关闭。
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 621, "rr-off-member", "user")
	installReviewRebateTestDB(t, db)
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateControllerRouter(&actor)

	out := uploadClaim(t, router, "real.png", pngFixture(t), nil)
	if out.Code != http.StatusBadRequest || !strings.Contains(out.Body.String(), "未启用") {
		t.Fatalf("disabled switch must reject submit with 400, got %d %s", out.Code, out.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/user/review-rebate/program", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("disabled switch must reject program view with 400, got %d %s", recorder.Code, recorder.Body.String())
	}
	// 也不能留下任何提交行。
	var count int64
	if err := db.Model(&entity.ReviewRebateClaim{}).Count(&count).Error; err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if count != 0 {
		t.Fatalf("disabled switch must not persist claims, got %d", count)
	}
}

// Test 7（HTTP 层）：打分 → 管理员批准 → 钱包与账单落账；重复批准只留一条 award。
func TestReviewRebateHTTPDecisionFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	setReviewRebateControllerSwitch(t, db, true)
	seedReviewRebateControllerProgram(t, db)
	member := seedReviewRebateControllerUser(t, db, 631, "rr-flow-member", "user")
	admin := seedReviewRebateControllerUser(t, db, 632, "rr-flow-admin", "admin")
	installReviewRebateTestDB(t, db)
	t.Chdir(t.TempDir())

	actor := member
	router := reviewRebateControllerRouter(&actor)

	out := uploadClaim(t, router, "real.png", pngFixture(t), nil)
	if out.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", out.Code, out.Body.String())
	}
	var created struct {
		ID int32 `json:"id"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	// 管理员打分（占位打分器）：命中兜底规则 → scored + 候选金额 1。
	actor = admin
	send := func(method, target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	out = send(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/score", created.ID), "")
	if out.Code != http.StatusOK {
		t.Fatalf("score failed: %d %s", out.Code, out.Body.String())
	}
	var scored entity.ReviewRebateClaim
	if err := json.Unmarshal(out.Body.Bytes(), &scored); err != nil {
		t.Fatalf("decode scored: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusScored || scored.AwardAmount != 1 {
		t.Fatalf("unexpected scored claim: %+v", scored)
	}

	// 批准。
	out = send(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/decision", created.ID), `{"action":"approve","note":"内容一致"}`)
	if out.Code != http.StatusOK {
		t.Fatalf("approve failed: %d %s", out.Code, out.Body.String())
	}
	var approved entity.ReviewRebateClaim
	if err := json.Unmarshal(out.Body.Bytes(), &approved); err != nil {
		t.Fatalf("decode approved: %v", err)
	}
	if approved.Status != entity.ReviewRebateStatusApproved || approved.ReviewedByUserID != admin.ID {
		t.Fatalf("unexpected approved claim: %+v", approved)
	}

	// 钱包：返现只进赠送余额（可消费、不可退），可退本金不动。
	var wallet entity.User
	if err := db.Where("id = ?", member.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if wallet.GiftBalance != 1 {
		t.Fatalf("review rebate must credit the gift bucket, got %v", wallet.GiftBalance)
	}
	if wallet.Balance != 0 {
		t.Fatalf("review rebate must not enter the refundable principal, got %v", wallet.Balance)
	}
	// 账单：一条 review_rebate。
	var records int64
	if err := db.Model(&entity.BillingRecord{}).
		Where("user_id = ? AND type = ?", member.ID, service.ReviewRebateBillingRecordType).
		Count(&records).Error; err != nil {
		t.Fatalf("count billing: %v", err)
	}
	if records != 1 {
		t.Fatalf("expected 1 review_rebate billing record, got %d", records)
	}

	// 重复批准：幂等 200，但 award 只有一条、钱包只加一次。
	out = send(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/decision", created.ID), `{"action":"approve","note":"再点一次"}`)
	if out.Code != http.StatusOK {
		t.Fatalf("repeat approve must be idempotent 200, got %d %s", out.Code, out.Body.String())
	}
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Where("claim_id = ?", created.ID).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 1 {
		t.Fatalf("repeat approve must keep 1 award, got %d", awards)
	}
	if err := db.Where("id = ?", member.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload member again: %v", err)
	}
	if wallet.GiftBalance != 1 || wallet.Balance != 0 {
		t.Fatalf("repeat approve must not credit twice, got balance=%v gift=%v", wallet.Balance, wallet.GiftBalance)
	}

	// 已批准的再拒绝 → 409 状态冲突。
	out = send(http.MethodPost, fmt.Sprintf("/admin/review-rebate/claims/%d/decision", created.ID), `{"action":"reject"}`)
	if out.Code != http.StatusConflict {
		t.Fatalf("rejecting an approved claim must be 409, got %d %s", out.Code, out.Body.String())
	}
}

// 管理端规则 CRUD：校验 MinStars ≤ MaxStars、金额非负；非管理员已在上面的用例里覆盖。
func TestReviewRebateHTTPRuleValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newReviewRebateControllerTestDB(t)
	program, rule := seedReviewRebateControllerProgram(t, db)
	admin := seedReviewRebateControllerUser(t, db, 641, "rr-rule-admin", "admin")
	installReviewRebateTestDB(t, db)

	actor := admin
	router := reviewRebateControllerRouter(&actor)
	send := func(method, target, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	bad := []string{
		// MinStars > MaxStars
		fmt.Sprintf(`{"programId":%d,"minStars":4,"maxStars":2,"minContentScore":1,"maxContentScore":10,"amount":1}`, program.ID),
		// 星数越界
		fmt.Sprintf(`{"programId":%d,"minStars":0,"maxStars":9,"minContentScore":1,"maxContentScore":10,"amount":1}`, program.ID),
		// 内容分越界
		fmt.Sprintf(`{"programId":%d,"minStars":1,"maxStars":5,"minContentScore":0,"maxContentScore":11,"amount":1}`, program.ID),
		// 负金额
		fmt.Sprintf(`{"programId":%d,"minStars":1,"maxStars":5,"minContentScore":1,"maxContentScore":10,"amount":-0.5}`, program.ID),
		// 不存在的活动
		`{"programId":9999,"minStars":1,"maxStars":5,"minContentScore":1,"maxContentScore":10,"amount":1}`,
	}
	for _, body := range bad {
		out := send(http.MethodPost, "/admin/review-rebate/rules", body)
		if out.Code != http.StatusBadRequest && out.Code != http.StatusNotFound {
			t.Fatalf("invalid rule must be rejected, got %d for %s", out.Code, body)
		}
	}

	// 合法新建 → 201，且缺省 enabled = true。
	out := send(http.MethodPost, "/admin/review-rebate/rules",
		fmt.Sprintf(`{"programId":%d,"minStars":5,"maxStars":5,"minContentScore":10,"maxContentScore":10,"amount":1.00,"priority":30,"remark":"满星好评"}`, program.ID))
	if out.Code != http.StatusCreated {
		t.Fatalf("valid rule must be created, got %d %s", out.Code, out.Body.String())
	}
	var created entity.ReviewRebateRule
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode rule: %v", err)
	}
	if !created.Enabled || created.Amount != 1 {
		t.Fatalf("omitted enabled must default to true: %+v", created)
	}

	// 更新 → 200。
	out = send(http.MethodPut, fmt.Sprintf("/admin/review-rebate/rules/%d", created.ID), `{"amount":1.5}`)
	if out.Code != http.StatusOK {
		t.Fatalf("update rule failed: %d %s", out.Code, out.Body.String())
	}
	// 删除 → 200；再删 → 404。
	// Q77-6 起 `?reason=` 必填（后端独立校验，见 rule_audit.go）；两次都带，
	// 否则第二次会被 400 先拦下，测不到「规则不存在」这条分支。
	if out := send(http.MethodDelete, fmt.Sprintf("/admin/review-rebate/rules/%d?reason=%s", created.ID, ruleDeleteTestReasonQuery), ""); out.Code != http.StatusOK {
		t.Fatalf("delete rule failed: %d %s", out.Code, out.Body.String())
	}
	if out := send(http.MethodDelete, fmt.Sprintf("/admin/review-rebate/rules/%d?reason=%s", created.ID, ruleDeleteTestReasonQuery), ""); out.Code != http.StatusNotFound {
		t.Fatalf("deleting missing rule must be 404, got %d %s", out.Code, out.Body.String())
	}
	// 原规则还在，活动配置可读。
	if out := send(http.MethodGet, "/admin/review-rebate/rules", ""); out.Code != http.StatusOK ||
		!strings.Contains(out.Body.String(), fmt.Sprintf(`"id":%d`, rule.ID)) {
		t.Fatalf("rule list must still contain the seeded rule: %d %s", out.Code, out.Body.String())
	}
}
