package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestAIModelArchitectureListReturnsScopedItems(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAIModelArchitectures
	defer func() { listAIModelArchitectures = original }()

	listAIModelArchitectures = func(user entity.User) ([]entity.AIModelArchitecture, error) {
		if user.ID != 7 || user.Role != "admin" {
			t.Fatalf("unexpected user: %+v", user)
		}
		return []entity.AIModelArchitecture{{ID: 1, Name: "GPT-4o", Enabled: true, Sort: 10}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-model-architectures", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "admin", Status: "enabled"})

	AIModelArchitectureList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var items []entity.AIModelArchitecture
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 1 || items[0].Name != "GPT-4o" {
		t.Fatalf("unexpected architectures: %+v", items)
	}
}

func TestCreateAIModelArchitectureRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-architectures", bytes.NewBufferString(`{"name":"GPT-4o"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	CreateAIModelArchitecture(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAIModelArchitectureReturnsCreatedItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createAIModelArchitecture
	defer func() { createAIModelArchitecture = original }()

	createAIModelArchitecture = func(user entity.User, input service.CreateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
		if user.ID != 9 || user.Role != "admin" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if input.Name != "GPT-4o" || input.ModelName != "gpt-4o" || input.Enabled != true || input.Remark != "常用" || input.Sort != 2 {
			t.Fatalf("unexpected input: %+v", input)
		}
		return entity.AIModelArchitecture{ID: 11, Name: input.Name, ModelName: input.ModelName, Enabled: input.Enabled, Sort: input.Sort}, nil
	}

	// 旧的计费倍率字段即使仍被前端传来，也必须被忽略：架构表不再参与计费。
	payload := []byte(`{"name":"GPT-4o","modelName":"gpt-4o","billingMultiplier":2.5,"enabled":true,"sort":2,"remark":"常用"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-model-architectures", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 9, Role: "admin", Status: "enabled"})

	CreateAIModelArchitecture(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var architecture entity.AIModelArchitecture
	if err := json.Unmarshal(recorder.Body.Bytes(), &architecture); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if architecture.ID != 11 || architecture.ModelName != "gpt-4o" {
		t.Fatalf("unexpected created architecture: %+v", architecture)
	}
	if strings.Contains(recorder.Body.String(), "billingMultiplier") {
		t.Fatalf("expected billing multiplier to be gone from response, got %s", recorder.Body.String())
	}
}

func TestUpdateAIModelArchitectureHandlesNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := updateAIModelArchitecture
	defer func() { updateAIModelArchitecture = original }()

	updateAIModelArchitecture = func(user entity.User, architectureID int32, input service.UpdateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
		return entity.AIModelArchitecture{}, service.ErrAIModelArchitectureNotFound
	}

	payload := []byte(`{"name":"GPT-4o","modelName":"gpt-4o","enabled":true}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/ai-model-architectures/3", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "3"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdateAIModelArchitecture(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeleteAIModelArchitectureDeletesItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := deleteAIModelArchitecture
	defer func() { deleteAIModelArchitecture = original }()

	called := false
	deleteAIModelArchitecture = func(user entity.User, architectureID int32) error {
		called = true
		if user.ID != 1 || user.Role != "admin" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if architectureID != 13 {
			t.Fatalf("expected architecture id 13, got %d", architectureID)
		}
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/adapter-service/ai-model-architectures/13", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "13"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	DeleteAIModelArchitecture(ctx)

	if !called {
		t.Fatal("expected delete seam to be called")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestAIModelArchitectureListReturnsServerErrorWhenLoadFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listAIModelArchitectures
	defer func() { listAIModelArchitectures = original }()

	listAIModelArchitectures = func(user entity.User) ([]entity.AIModelArchitecture, error) {
		return nil, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-model-architectures", nil)
	ctx.Set("user", &entity.User{ID: 10, Role: "admin", Status: "enabled"})

	AIModelArchitectureList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
