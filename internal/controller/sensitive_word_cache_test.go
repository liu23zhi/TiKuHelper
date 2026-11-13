package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 敏感词检测记录管理接口的 controller 层测试（123 号 T-27）。
//
// 与 service 层测试的分工：
//   - service 测试钉「三态判定 / 原文不外泄 / 二次检测零外呼 / hash 域分隔」；
//   - 这里钉「HTTP 层」：鉴权、分页参数校验、状态筛选参数、下发 JSON 不含原文。

func newSensitiveWordCacheControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:sw-cache-controller-%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SensitiveWordCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// withSensitiveWordCacheDB 把 controller 的 DB 句柄换成测试库。
//
// 生产实现走 resolveSearchPersistenceDB()（dao.Log 或 manager），
// 测试里直接注入，避免依赖全局 manager 状态。
func withSensitiveWordCacheDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	original := resolveSearchPersistenceDBForSensitiveWordCache
	resolveSearchPersistenceDBForSensitiveWordCache = func() *gorm.DB { return db }
	t.Cleanup(func() { resolveSearchPersistenceDBForSensitiveWordCache = original })
}

// callSensitiveWordCache 以指定角色调用一个 handler。
func callSensitiveWordCache(t *testing.T, handler gin.HandlerFunc, query string, role string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/admin/sensitive-word-cache"+query, nil)
	if role != "" {
		c.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
	}
	handler(c)
	return recorder
}

// TestSensitiveWordCacheEndpointsRequireAdminBeforeDatabaseAccess 钉鉴权顺序。
//
// 关键：未授权请求必须在**碰数据库之前**就被拦下。
// 这里把 DB 句柄换成一个一旦被调用就 t.Fatal 的桩 —— 若实现先查库再鉴权，
// 这条会直接失败（而不是悄悄多查一次）。
func TestSensitiveWordCacheEndpointsRequireAdminBeforeDatabaseAccess(t *testing.T) {
	original := resolveSearchPersistenceDBForSensitiveWordCache
	resolveSearchPersistenceDBForSensitiveWordCache = func() *gorm.DB {
		t.Fatal("未授权的敏感词记录请求碰到了数据库")
		return nil
	}
	t.Cleanup(func() { resolveSearchPersistenceDBForSensitiveWordCache = original })

	for _, handler := range []gin.HandlerFunc{SensitiveWordCacheList, SensitiveWordCacheStatus} {
		for role, want := range map[string]int{"": http.StatusUnauthorized, "user": http.StatusForbidden} {
			response := callSensitiveWordCache(t, handler, "", role)
			if response.Code != want {
				t.Fatalf("role=%q status=%d want=%d body=%s", role, response.Code, want, response.Body.String())
			}
		}
	}
}

// TestSensitiveWordCacheListSupportsThreeStateFilter 是判据 ① 的 HTTP 层钉子。
func TestSensitiveWordCacheListSupportsThreeStateFilter(t *testing.T) {
	db := newSensitiveWordCacheControllerTestDB(t)
	withSensitiveWordCacheDB(t, db)
	seed := func(hash string, sensitive bool, masked, status, errText string) {
		if err := db.Create(&entity.SensitiveWordCache{
			TextHash: hash, Sensitive: sensitive, Words: "[]", MaskedText: masked,
			APIStatus: status, Error: errText,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed(strings.Repeat("a", 64), true, "**题干", "forbidden", "")
	seed(strings.Repeat("b", 64), false, "干净题干", "clean", "")
	seed(strings.Repeat("c", 64), false, "", "", "upstream 500")

	// 全部
	all := callSensitiveWordCache(t, SensitiveWordCacheList, "", "admin")
	if all.Code != http.StatusOK {
		t.Fatalf("list all status=%d body=%s", all.Code, all.Body.String())
	}
	var allPayload struct {
		Data  []map[string]any `json:"data"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(all.Body.Bytes(), &allPayload); err != nil {
		t.Fatalf("decode: %v body=%s", err, all.Body.String())
	}
	if allPayload.Total != 3 || len(allPayload.Data) != 3 {
		t.Fatalf("应返回 3 条，实际 total=%d len=%d", allPayload.Total, len(allPayload.Data))
	}

	for state, wantHash := range map[string]string{
		"hit":    strings.Repeat("a", 64),
		"clean":  strings.Repeat("b", 64),
		"failed": strings.Repeat("c", 64),
	} {
		response := callSensitiveWordCache(t, SensitiveWordCacheList, "?state="+state, "admin")
		if response.Code != http.StatusOK {
			t.Fatalf("state=%s status=%d body=%s", state, response.Code, response.Body.String())
		}
		var payload struct {
			Data  []map[string]any `json:"data"`
			Total int64            `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode state=%s: %v", state, err)
		}
		if payload.Total != 1 || len(payload.Data) != 1 {
			t.Fatalf("state=%s 应恰好 1 条，实际 total=%d len=%d", state, payload.Total, len(payload.Data))
		}
		if payload.Data[0]["textHash"] != wantHash {
			t.Fatalf("state=%s 取到错误的行：%v", state, payload.Data[0]["textHash"])
		}
		if payload.Data[0]["state"] != state {
			t.Fatalf("state 字段应为 %s，实际 %v", state, payload.Data[0]["state"])
		}
	}
}

// TestSensitiveWordCacheListRejectsBadParams 钉参数校验是 400（不是 500、也不是静默忽略）。
func TestSensitiveWordCacheListRejectsBadParams(t *testing.T) {
	db := newSensitiveWordCacheControllerTestDB(t)
	withSensitiveWordCacheDB(t, db)

	for _, query := range []string{
		"?state=bogus",
		"?page=0",
		"?pageSize=0",
		"?pageSize=101",
		"?page=abc",
		"?checkedFrom=not-a-date",
		"?checkedFrom=2026-02-01&checkedTo=2026-01-01",
	} {
		response := callSensitiveWordCache(t, SensitiveWordCacheList, query, "admin")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query=%q 应返回 400，实际 %d body=%s", query, response.Code, response.Body.String())
		}
	}
	// 合法边界值必须放行。
	for _, query := range []string{"?page=1&pageSize=100", "?state=HIT", "?state=%20clean%20"} {
		response := callSensitiveWordCache(t, SensitiveWordCacheList, query, "admin")
		if response.Code != http.StatusOK {
			t.Fatalf("query=%q 应放行，实际 %d body=%s", query, response.Code, response.Body.String())
		}
	}
}

// TestSensitiveWordCacheListExposesMaskedButNotOriginal 是判据 ② 的 HTTP 级钉子。
//
// 「MaskedText 可看、原文不可看，且不是靠前端隐藏」：
// 断言下发 JSON 里**没有任何字段**携带原文，并且 maskedText 确实在。
// 这不是前端 v-if 隐藏能做到的 —— 原文根本不会出现在响应体里。
func TestSensitiveWordCacheListExposesMaskedButNotOriginal(t *testing.T) {
	db := newSensitiveWordCacheControllerTestDB(t)
	withSensitiveWordCacheDB(t, db)
	const original = "原始题目文本不应出现在响应里"
	const masked = "原始题目****不应出现在响应里"
	if err := db.Create(&entity.SensitiveWordCache{
		TextHash: strings.Repeat("d", 64), Sensitive: true, Words: `["敏感"]`,
		MaskedText: masked, APIStatus: "forbidden",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	response := callSensitiveWordCache(t, SensitiveWordCacheList, "", "admin")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, original) {
		t.Fatalf("响应体不得包含原文：%s", body)
	}
	if !strings.Contains(body, masked) {
		t.Fatalf("响应体必须保留 maskedText 供排障：%s", body)
	}
	// 字段级：不得出现原文类键名。
	for _, forbidden := range []string{`"question"`, `"answer"`, `"originalText"`, `"rawText"`, `"original_text"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("响应体不得出现字段 %s：%s", forbidden, body)
		}
	}
	// 必须能看到的几个字段。
	for _, expected := range []string{`"textHash"`, `"maskedText"`, `"state"`, `"sensitive"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("响应体应包含字段 %s：%s", expected, body)
		}
	}
}

// TestSensitiveWordCacheStatusReportsThreeStates 钉状态接口。
func TestSensitiveWordCacheStatusReportsThreeStates(t *testing.T) {
	db := newSensitiveWordCacheControllerTestDB(t)
	withSensitiveWordCacheDB(t, db)
	for _, row := range []entity.SensitiveWordCache{
		{TextHash: strings.Repeat("e", 64), Sensitive: true, Words: "[]", APIStatus: "forbidden"},
		{TextHash: strings.Repeat("f", 64), Sensitive: false, Words: "[]", APIStatus: "clean"},
		{TextHash: strings.Repeat("0", 64), Sensitive: false, Words: "[]", Error: "boom"},
	} {
		entry := row
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	response := callSensitiveWordCache(t, SensitiveWordCacheStatus, "", "admin")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		States struct {
			Hit    int64 `json:"hit"`
			Clean  int64 `json:"clean"`
			Failed int64 `json:"failed"`
		} `json:"states"`
		LatestCheckedAt any `json:"latestCheckedAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, response.Body.String())
	}
	if payload.States.Hit != 1 || payload.States.Clean != 1 || payload.States.Failed != 1 {
		t.Fatalf("三态计数不对：%+v", payload.States)
	}
	if payload.LatestCheckedAt == nil {
		t.Fatalf("有记录时 latestCheckedAt 不应为 null")
	}
}

// TestSensitiveWordCacheListHandlesMissingTable 保证表没迁移时列表仍可用（空结果而不是 500）。
func TestSensitiveWordCacheListHandlesMissingTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:sw-cache-empty-%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	withSensitiveWordCacheDB(t, db)
	response := callSensitiveWordCache(t, SensitiveWordCacheList, "", "admin")
	if response.Code != http.StatusOK {
		t.Fatalf("缺表时应返回 200 空列表，实际 %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data  []map[string]any `json:"data"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Total != 0 || len(payload.Data) != 0 {
		t.Fatalf("缺表时总数必须为 0：total=%d len=%d", payload.Total, len(payload.Data))
	}
}
