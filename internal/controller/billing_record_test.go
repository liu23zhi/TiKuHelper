package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestBillingRecordListReturnsScopedRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
		if user.ID != 7 {
			t.Fatalf("expected user id 7, got %d", user.ID)
		}
		if page != 2 || pageSize != 5 {
			t.Fatalf("expected pagination 2/5, got %d/%d", page, pageSize)
		}
		return service.BillingRecordListResult{
			Data:     []service.BillingRecordListItem{{ID: 1, Provider: "icodef", ProviderLabel: "Icodef 题库", Amount: 1.25, AccessTokenName: "默认令牌", CreatedAt: time.Unix(0, 0)}},
			Total:    1,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records?page=2&pageSize=5", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "user", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response service.BillingRecordListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 1 || len(response.Data) != 1 || response.Data[0].ProviderLabel != "Icodef 题库" {
		t.Fatalf("unexpected billing response: %+v", response)
	}
	if response.Data[0].AccessTokenName != "默认令牌" {
		t.Fatalf("expected access token name 默认令牌, got %+v", response.Data[0])
	}
	if response.Page != 2 || response.PageSize != 5 {
		t.Fatalf("expected pagination 2/5 in response, got %d/%d", response.Page, response.PageSize)
	}
}

func TestBillingRecordListNormalizesInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
		if page != 1 || pageSize != 10 {
			t.Fatalf("expected normalized pagination 1/10, got %d/%d", page, pageSize)
		}
		return service.BillingRecordListResult{Data: []service.BillingRecordListItem{}, Total: 0, Page: page, PageSize: pageSize}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records?page=0&pageSize=1000", nil)
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestBillingRecordListRejectsReversedCreatedTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 8, Role: "admin", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records?createdFrom=2026-06-21&createdTo=2026-06-20", nil)
	BillingRecordList(ctx)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "创建时间范围无效") {
		t.Fatalf("reversed billing range must return explicit 400, status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBillingRecordListForcesOwnScopeForUsagePage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
		if user.Role == "admin" {
			return service.BillingRecordListResult{
				Data: []service.BillingRecordListItem{
					{ID: 101, Provider: "icodef"},
					{ID: 202, Provider: "icodef"},
				},
				Total:    2,
				Page:     page,
				PageSize: pageSize,
			}, nil
		}
		return service.BillingRecordListResult{
			Data:     []service.BillingRecordListItem{{ID: 101, Provider: "icodef"}},
			Total:    1,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records?page=1&pageSize=10&scope=self", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "admin", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response service.BillingRecordListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("expected admin personal usage page to show only own record, got %+v", response.Data)
	}
	if response.Total != 1 || response.Data[0].ID != 101 {
		t.Fatalf("unexpected personal usage response: %+v", response)
	}
}

func TestBillingRecordListRedactsAdminSelfScopeAndPreservesAdminDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:billing-controller-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open billing db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get billing db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate billing db: %v", err)
	}
	admin := entity.User{ID: 7, Username: "billing-admin", Password: "test", Role: "admin", Status: "enabled", RegisterSource: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	charge := entity.BillingRecord{
		UserID: admin.ID, Type: "normal_search", Provider: "ai", ModelName: "private-test-model", Amount: 0.3,
		InputTokens: 100, CachedInputTokens: 10, TotalTokens: 100, RequestID: "req-admin-self", QuestionText: "测试题目",
		UnitPriceSnapshot: "provider=ai multiplier=2 stageBillingCall1=" + url.QueryEscape("index=1,model=private-test-model,inputTokens=100,cachedInputTokens=10,totalTokens=100,amount=0.3,stageMultiplier=2,finalMultiplier=2"),
	}
	if err := db.Create(&charge).Error; err != nil {
		t.Fatalf("create charge: %v", err)
	}
	original := listBillingRecords
	defer func() { listBillingRecords = original }()
	listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
		return service.ListBillingRecordsWithDB(db, user, query, page, pageSize)
	}

	for _, scope := range []string{"self", "all"} {
		t.Run(scope, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records?scope="+scope, nil)
			ctx.Set("user", &admin)
			BillingRecordList(ctx)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			var response struct {
				Data []struct {
					FeeDetails []map[string]interface{} `json:"feeDetails"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode fee details: %v", err)
			}
			if len(response.Data) != 1 || len(response.Data[0].FeeDetails) != 1 {
				t.Fatalf("expected one model call detail, got %s", body)
			}
			cacheHitRate, hasCacheHitRate := response.Data[0].FeeDetails[0]["cacheHitRate"]
			if scope == "self" {
				if hasCacheHitRate {
					t.Fatalf("personal scope exposed per-call cacheHitRate: %s", body)
				}
				for _, forbidden := range []string{"effectiveMultiplier", "finalMultiplier", "stageMultiplier", "modelName", "inputTokens", "totalTokens", "private-test-model", "unitPriceSnapshot"} {
					if strings.Contains(body, forbidden) {
						t.Fatalf("personal scope exposed %q: %s", forbidden, body)
					}
				}
			} else {
				if !hasCacheHitRate || cacheHitRate != 0.1 {
					t.Fatalf("admin scope must expose per-call cacheHitRate 0.1, got %v: %s", cacheHitRate, body)
				}
				for _, expected := range []string{"effectiveMultiplier", "finalMultiplier", "stageMultiplier", "totalTokens", "private-test-model"} {
					if !strings.Contains(body, expected) {
						t.Fatalf("admin scope lost %q: %s", expected, body)
					}
				}
			}
		})
	}
}

func TestBillingRecordListReturnsServerErrorOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, query service.BillingRecordListQuery, page int, pageSize int) (service.BillingRecordListResult, error) {
		return service.BillingRecordListResult{}, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/billing-records", nil)
	ctx.Set("user", &entity.User{ID: 9, Role: "user", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
