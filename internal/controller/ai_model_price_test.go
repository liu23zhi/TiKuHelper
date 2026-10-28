package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 价格表没有状态概念，普通用户与管理员拿到的是同一份全量表。
func TestAIModelPriceListReturnsAllRowsRegardlessOfRole(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAIModelPrices
	defer func() { listAIModelPrices = original }()

	calls := 0
	listAIModelPrices = func() ([]entity.AIModelPrice, error) {
		calls++
		return []entity.AIModelPrice{{ID: 1, ModelName: "gpt-5.5", DisplayName: "GPT-5.5", InputPrice: 5, CachedPrice: 0.5, OutputPrice: 30, Status: "enabled"}}, nil
	}

	for _, role := range []string{"user", "admin"} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-model-prices", nil)
		ctx.Set("user", &entity.User{ID: 8, Role: role, Status: "enabled"})

		AIModelPriceList(ctx)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected status 200 for %s, got %d with body %s", role, recorder.Code, recorder.Body.String())
		}

		var items []entity.AIModelPrice
		if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(items) != 1 || items[0].ModelName != "gpt-5.5" || items[0].OutputPrice != 30 {
			t.Fatalf("unexpected model prices for %s: %+v", role, items)
		}
	}

	if calls != 2 {
		t.Fatalf("expected the price table to be listed once per request, got %d", calls)
	}
}

func TestAIModelPriceListReturnsEmptyArrayInsteadOfNull(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAIModelPrices
	defer func() { listAIModelPrices = original }()

	listAIModelPrices = func() ([]entity.AIModelPrice, error) { return nil, nil }

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-model-prices", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	AIModelPriceList(ctx)

	if body := recorder.Body.String(); body != "[]" {
		t.Fatalf("expected empty JSON array, got %q", body)
	}
}

func TestCreateAIModelPriceRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices", bytes.NewBufferString(`{"modelName":"gpt-5.5"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	CreateAIModelPrice(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAIModelPriceReturnsCreatedItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createAIModelPrice
	defer func() { createAIModelPrice = original }()

	createAIModelPrice = func(input service.CreateAIModelPriceInput) (entity.AIModelPrice, error) {
		if input.ModelName != "daybreak-red" || input.OutputPrice != 75 {
			t.Fatalf("unexpected input: %+v", input)
		}
		return entity.AIModelPrice{ID: 9, ModelName: input.ModelName, OutputPrice: input.OutputPrice, Status: "enabled"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices", bytes.NewBufferString(`{"modelName":"daybreak-red","outputPrice":75}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	CreateAIModelPrice(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var created entity.AIModelPrice
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ID != 9 || created.ModelName != "daybreak-red" {
		t.Fatalf("unexpected created price: %+v", created)
	}
}

func TestCreateAIModelPriceTreatsMissingNameAsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createAIModelPrice
	defer func() { createAIModelPrice = original }()

	createAIModelPrice = func(service.CreateAIModelPriceInput) (entity.AIModelPrice, error) {
		return entity.AIModelPrice{}, service.ErrAIModelPriceNameRequired
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices", bytes.NewBufferString(`{"modelName":""}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	CreateAIModelPrice(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAIModelPricePassesContextAndRejectsInvalidWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := createAIModelPrice
	defer func() { createAIModelPrice = original }()
	createAIModelPrice = func(input service.CreateAIModelPriceInput) (entity.AIModelPrice, error) {
		if input.ContextWindowTokens == nil || *input.ContextWindowTokens != 128000 {
			t.Fatalf("context window was not parsed: %+v", input)
		}
		return entity.AIModelPrice{ModelName: input.ModelName, ContextWindowTokens: *input.ContextWindowTokens}, nil
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices", bytes.NewBufferString(`{"modelName":"fixture","contextWindowTokens":128000}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	CreateAIModelPrice(ctx)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"contextWindowTokens":128000`)) {
		t.Fatalf("context not returned: %d %s", recorder.Code, recorder.Body.String())
	}

	createAIModelPrice = func(service.CreateAIModelPriceInput) (entity.AIModelPrice, error) {
		return entity.AIModelPrice{}, service.ErrAIModelContextWindowInvalid
	}
	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices", bytes.NewBufferString(`{"modelName":"fixture","contextWindowTokens":-1}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	CreateAIModelPrice(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid context should be bad request: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateAIModelPriceHandlesNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := updateAIModelPrice
	defer func() { updateAIModelPrice = original }()

	updateAIModelPrice = func(int32, service.UpdateAIModelPriceInput) (entity.AIModelPrice, error) {
		return entity.AIModelPrice{}, service.ErrAIModelPriceNotFound
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/ai-model-prices/404", bytes.NewBufferString(`{"modelName":"GPT-5.5"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "404"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdateAIModelPrice(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeleteAIModelPriceDeletesItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := deleteAIModelPrice
	defer func() { deleteAIModelPrice = original }()

	var deletedID int32
	deleteAIModelPrice = func(priceID int32) error {
		deletedID = priceID
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/adapter-service/ai-model-prices/12", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "12"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	DeleteAIModelPrice(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if deletedID != 12 {
		t.Fatalf("expected price 12 to be deleted, got %d", deletedID)
	}
}

func TestResetAIModelPriceDefaultsRequiresAdminAndDelegates(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := resetAIModelPrices
	defer func() { resetAIModelPrices = original }()

	called := false
	resetAIModelPrices = func() error {
		called = true
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices/reset", nil)
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	ResetAIModelPriceDefaults(ctx)

	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("expected non-admin reset to be rejected, got %d called=%t", recorder.Code, called)
	}

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-prices/reset", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	ResetAIModelPriceDefaults(ctx)

	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("expected admin reset to succeed, got %d called=%t", recorder.Code, called)
	}
}

func TestAIModelPriceListReturnsServerErrorWhenLoadFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAIModelPrices
	defer func() { listAIModelPrices = original }()

	listAIModelPrices = func() ([]entity.AIModelPrice, error) {
		return nil, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-model-prices", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	AIModelPriceList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
