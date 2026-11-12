package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 手动补录返佣 + 赠送记录聚合的 HTTP 层契约（B1 + B2）。
//
// 与 service 层用例的分工：这里只测「权限、状态码、错误映射、JSON 字段名」，
// 不重复测钱包数值（那是 service 用例的职责）。两者都要有，
// 因为「权限没加」和「入账算错」是两个不同的缺陷面。

// newGiftRecordControllerTestDB 建库并补齐账单表（newReferralControllerTestDB 不含它）。
func newGiftRecordControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newReferralControllerTestDB(t)
	if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate billing table: %v", err)
	}
	return db
}

// TestManualReferralRebateEndpointRequiresAdmin 补录接口必须仅管理员。
func TestManualReferralRebateEndpointRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newGiftRecordControllerTestDB(t)
	setReferralControllerSettings(t, db, true, 0.1)
	admin := seedReferralControllerUser(t, db, 701, "gift-admin", "admin", 0)
	member := seedReferralControllerUser(t, db, 702, "gift-member", "user", 0)

	router := gin.New()
	actor := member
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	// 与 router.go 完全一致的中间件顺序：Auth → RequireAdmin。
	router.POST("/admin/referral-rebates", auth, middleware.RequireAdmin, CreateReferralRebate)
	router.GET("/admin/gift-records", auth, middleware.RequireAdmin, GiftRecordList)

	previousDB := referralDB
	referralDB = func() *gorm.DB { return db }
	t.Cleanup(func() { referralDB = previousDB })

	body := `{"beneficiaryUserId":702,"amount":10,"day":"2026-11-05"}`
	send := func(method, target, payload string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		var reader *strings.Reader
		if payload == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(payload)
		}
		request := httptest.NewRequest(method, target, reader)
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(out, request)
		return out
	}

	// 普通用户两个接口都必须 403。
	if out := send(http.MethodPost, "/admin/referral-rebates", body); out.Code != http.StatusForbidden {
		t.Fatalf("non-admin manual rebate must be 403, got %d %s", out.Code, out.Body.String())
	}
	if out := send(http.MethodGet, "/admin/gift-records", ""); out.Code != http.StatusForbidden {
		t.Fatalf("non-admin gift records must be 403, got %d %s", out.Code, out.Body.String())
	}
	// 且非管理员的请求一个字节都不能落库。
	var count int64
	if err := db.Model(&entity.ReferralRebate{}).Count(&count).Error; err != nil {
		t.Fatalf("count rebates: %v", err)
	}
	if count != 0 {
		t.Fatalf("forbidden request must not create a rebate, got %d", count)
	}

	// 管理员：200 + source=manual + 入账。
	actor = admin
	out := send(http.MethodPost, "/admin/referral-rebates", body)
	if out.Code != http.StatusOK {
		t.Fatalf("admin manual rebate must be 200, got %d %s", out.Code, out.Body.String())
	}
	var created entity.ReferralRebate
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode rebate: %v body=%s", err, out.Body.String())
	}
	if created.Source != entity.ReferralRebateSourceManual {
		t.Fatalf("created rebate source must be manual, got %q", created.Source)
	}
	var beneficiary entity.User
	if err := db.Where("id = ?", member.ID).First(&beneficiary).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if beneficiary.GiftBalance != 10 {
		t.Fatalf("manual rebate must credit gift_balance with 10, got %v", beneficiary.GiftBalance)
	}

	// 重复提交必须 409（不是 200 静默成功），且余额不再涨。
	out = send(http.MethodPost, "/admin/referral-rebates", body)
	if out.Code != http.StatusConflict {
		t.Fatalf("duplicate manual rebate must be 409, got %d %s", out.Code, out.Body.String())
	}
	if err := db.Where("id = ?", member.ID).First(&beneficiary).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if beneficiary.GiftBalance != 10 {
		t.Fatalf("duplicate must not credit twice, got %v", beneficiary.GiftBalance)
	}
}

// TestManualReferralRebateEndpointErrorMapping 错误必须映射成可区分的状态码。
func TestManualReferralRebateEndpointErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newGiftRecordControllerTestDB(t)
	setReferralControllerSettings(t, db, true, 0.1)
	admin := seedReferralControllerUser(t, db, 703, "gift-admin-err", "admin", 0)

	router := gin.New()
	actor := admin
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	router.POST("/admin/referral-rebates", auth, middleware.RequireAdmin, CreateReferralRebate)

	previousDB := referralDB
	referralDB = func() *gorm.DB { return db }
	t.Cleanup(func() { referralDB = previousDB })

	cases := []struct {
		name     string
		payload  string
		wantCode int
	}{
		{"请求体非法", `not-json`, http.StatusBadRequest},
		{"金额为负", `{"beneficiaryUserId":703,"amount":-1}`, http.StatusBadRequest},
		{"日期格式错误", `{"beneficiaryUserId":703,"amount":1,"day":"2026/11/05"}`, http.StatusBadRequest},
		// 受益人不存在：管理员必须看到 404，而不是 500「补录失败」。
		{"受益人不存在", `{"beneficiaryUserId":9999,"amount":1}`, http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			out := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/admin/referral-rebates", bytes.NewBufferString(testCase.payload))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(out, request)
			if out.Code != testCase.wantCode {
				t.Fatalf("want %d, got %d body=%s", testCase.wantCode, out.Code, out.Body.String())
			}
		})
	}
}

// TestGiftRecordListEndpointRequiresAdminAndMapsSource 赠��记录：仅管理员 + 来源筛选 + 非法值 400。
func TestGiftRecordListEndpointRequiresAdminAndMapsSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newGiftRecordControllerTestDB(t)
	admin := seedReferralControllerUser(t, db, 704, "gift-admin-list", "admin", 0)
	member := seedReferralControllerUser(t, db, 705, "gift-member-list", "user", 0)

	// 造两条：一条邀请返佣、一条评价返现。
	for _, seed := range []struct {
		recordType string
		amount     float64
	}{
		{service.GiftRecordSourceReferralRebate, 30},
		{service.GiftRecordSourceReviewRebate, 20},
	} {
		record := entity.BillingRecord{
			UserID: member.ID, Type: seed.recordType, Provider: "system",
			Amount: seed.amount, RequestID: "gift-list-" + seed.recordType,
		}
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("seed billing: %v", err)
		}
	}

	router := gin.New()
	actor := member
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	router.GET("/admin/gift-records", auth, middleware.RequireAdmin, GiftRecordList)
	router.GET("/admin/gift-records/sources", auth, middleware.RequireAdmin, GiftRecordSourceList)

	previousDB := referralDB
	referralDB = func() *gorm.DB { return db }
	t.Cleanup(func() { referralDB = previousDB })

	send := func(target string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, target, nil))
		return out
	}

	if out := send("/admin/gift-records"); out.Code != http.StatusForbidden {
		t.Fatalf("non-admin must be 403, got %d", out.Code)
	}

	actor = admin
	out := send("/admin/gift-records")
	if out.Code != http.StatusOK {
		t.Fatalf("admin list must be 200, got %d %s", out.Code, out.Body.String())
	}
	var listed service.GiftRecordListResult
	if err := json.Unmarshal(out.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode gift records: %v body=%s", err, out.Body.String())
	}
	if listed.Total != 2 || listed.Summary.TotalAmount != 50 {
		t.Fatalf("expected 2 rows / 50, got total=%d amount=%v", listed.Total, listed.Summary.TotalAmount)
	}
	// 四类必须都在汇总里（补 0），前端图例才不会抖。
	if len(listed.Summary.BySource) != 4 {
		t.Fatalf("summary must list four sources, got %d", len(listed.Summary.BySource))
	}

	// 来源筛选生效。
	out = send("/admin/gift-records?source=" + service.GiftRecordSourceReferralRebate)
	if out.Code != http.StatusOK {
		t.Fatalf("source filter must be 200, got %d %s", out.Code, out.Body.String())
	}
	var filtered service.GiftRecordListResult
	if err := json.Unmarshal(out.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if filtered.Total != 1 || filtered.Summary.TotalAmount != 30 {
		t.Fatalf("source filter must match 1 row / 30, got total=%d amount=%v",
			filtered.Total, filtered.Summary.TotalAmount)
	}

	// 非法来源必须 400，不能静默当成「全部」（否则管理员以为筛过了）。
	if out := send("/admin/gift-records?source=bogus"); out.Code != http.StatusBadRequest {
		t.Fatalf("unknown source must be 400, got %d %s", out.Code, out.Body.String())
	}
	// 非法 userId / 时间也要 400。
	if out := send("/admin/gift-records?userId=abc"); out.Code != http.StatusBadRequest {
		t.Fatalf("bad userId must be 400, got %d", out.Code)
	}
	if out := send("/admin/gift-records?createdFrom=not-a-date"); out.Code != http.StatusBadRequest {
		t.Fatalf("bad createdFrom must be 400, got %d", out.Code)
	}
	// 区间反了也必须 400（start 不早于 end）。
	if out := send("/admin/gift-records?createdFrom=2026-11-05&createdTo=2026-11-01"); out.Code != http.StatusBadRequest {
		t.Fatalf("inverted range must be 400, got %d", out.Code)
	}

	// 来源下拉：四类 + 中文名。
	out = send("/admin/gift-records/sources")
	if out.Code != http.StatusOK {
		t.Fatalf("sources must be 200, got %d", out.Code)
	}
	var sourcePayload struct {
		Data []service.GiftRecordSourceSummary `json:"data"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &sourcePayload); err != nil {
		t.Fatalf("decode sources: %v", err)
	}
	if len(sourcePayload.Data) != 4 {
		t.Fatalf("sources must expose 4 entries, got %d", len(sourcePayload.Data))
	}
	for _, option := range sourcePayload.Data {
		if option.Label == "" {
			t.Fatalf("source %q must have a label", option.Source)
		}
	}
}
