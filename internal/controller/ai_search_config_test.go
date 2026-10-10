package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
)

func TestAISearchConfigTestRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-search-config/test", bytes.NewBufferString(`{"model":"gpt-4o-mini"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	TestAISearchConfig(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestAISearchConfigTestReturnsDebugPayloadForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := runAISearchDebug
	defer func() { runAISearchDebug = original }()

	runAISearchDebug = func(input aiSearchTestInput) ([][]string, internalsearch.AISearchDebugResult, error) {
		if input.BaseURL != "https://demo.example.com/v1" || input.APIKey != "sk-test" || input.Model != "gpt-4o-mini" || input.Prompt != "你是测试助手" {
			t.Fatalf("unexpected input: %+v", input)
		}
		return [][]string{{"hello"}}, internalsearch.AISearchDebugResult{
			Endpoint:    "https://demo.example.com/v1/chat/completions",
			RequestBody: `{"model":"gpt-4o-mini"}`,
			RawResponse: "data: {\"choices\":[]}\n\ndata: [DONE]",
			Usage: internalsearch.AISearchUsage{
				InputTokens:  3,
				OutputTokens: 1,
				TotalTokens:  4,
			},
		}, nil
	}

	payload := []byte(`{"baseUrl":"https://demo.example.com/v1","apiKey":"sk-test","model":"gpt-4o-mini","prompt":"你是测试助手"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-search-config/test", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	TestAISearchConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Answers     []string                     `json:"answers"`
		Endpoint    string                       `json:"endpoint"`
		RequestBody string                       `json:"requestBody"`
		RawResponse string                       `json:"rawResponse"`
		Usage       internalsearch.AISearchUsage `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Answers) != 1 || response.Answers[0] != "hello" {
		t.Fatalf("unexpected answers: %+v", response.Answers)
	}
	if response.Endpoint != "https://demo.example.com/v1/chat/completions" {
		t.Fatalf("unexpected endpoint: %s", response.Endpoint)
	}
	if response.RequestBody == "" || response.RawResponse == "" {
		t.Fatalf("expected request and response payloads, got %+v", response)
	}
	if response.Usage.TotalTokens != 4 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}
