package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestBillingRecordListReturnsScopedRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, page int, pageSize int) (service.BillingRecordListResult, error) {
		if user.ID != 7 {
			t.Fatalf("expected user id 7, got %d", user.ID)
		}
		if page != 2 || pageSize != 5 {
			t.Fatalf("expected pagination 2/5, got %d/%d", page, pageSize)
		}
		return service.BillingRecordListResult{
			Data: []service.BillingRecordListItem{{ID: 1, Provider: "icodef", ProviderLabel: "Icodef 题库", Amount: 1.25, AccessTokenName: "默认令牌", CreatedAt: time.Unix(0, 0)}},
			Total:    1,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/billing-records?page=2&pageSize=5", nil)
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

	listBillingRecords = func(user entity.User, page int, pageSize int) (service.BillingRecordListResult, error) {
		if page != 1 || pageSize != 10 {
			t.Fatalf("expected normalized pagination 1/10, got %d/%d", page, pageSize)
		}
		return service.BillingRecordListResult{Data: []service.BillingRecordListItem{}, Total: 0, Page: page, PageSize: pageSize}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/billing-records?page=0&pageSize=1000", nil)
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestBillingRecordListForcesOwnScopeForUsagePage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, page int, pageSize int) (service.BillingRecordListResult, error) {
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
			Data: []service.BillingRecordListItem{{ID: 101, Provider: "icodef"}},
			Total:    1,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/billing-records?page=1&pageSize=10&scope=self", nil)
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

func TestBillingRecordListReturnsServerErrorOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listBillingRecords
	defer func() {
		listBillingRecords = original
	}()

	listBillingRecords = func(user entity.User, page int, pageSize int) (service.BillingRecordListResult, error) {
		return service.BillingRecordListResult{}, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/billing-records", nil)
	ctx.Set("user", &entity.User{ID: 9, Role: "user", Status: "enabled"})

	BillingRecordList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
