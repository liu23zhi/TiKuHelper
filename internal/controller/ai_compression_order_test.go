package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestAICompressionOrderConfigHandlerPersistsAndPreservesLegacyUpdates(t *testing.T) {
	db := newSearchControllerTestDB(t)
	original := updateAISearchConfig
	t.Cleanup(func() { updateAISearchConfig = original })
	updateAISearchConfig = func(input service.UpdateAISearchConfigInput) (service.AISearchConfigView, error) {
		return service.UpdateAISearchConfigWithDB(db, input)
	}
	call := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/ai-search-config", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
		UpdateAISearchConfig(c)
		return recorder
	}
	want := []string{"local", "server", "remote"}
	for _, body := range []string{
		`{"contextCompressionOrder":["local","server","remote"]}`,
		`{"model":"legacy-model","useRemoteContextCompression":false}`,
	} {
		response := call(body)
		var view service.AISearchConfigView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || !reflect.DeepEqual(view.ContextCompressionOrder, want) {
			t.Fatalf("status=%d order=%v response=%s", response.Code, view.ContextCompressionOrder, response.Body)
		}
		stored, err := service.GetAISearchConfigWithDB(db)
		if err != nil || !reflect.DeepEqual(stored.ContextCompressionOrder, want) {
			t.Fatalf("read did not preserve API order: view=%+v err=%v", stored, err)
		}
	}
	response := call(`{"model":"must-not-persist","contextCompressionOrder":["server","server","local"]}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid order status=%d response=%s", response.Code, response.Body)
	}
	stored, err := service.GetAISearchConfigWithDB(db)
	if err != nil || stored.Model != "legacy-model" || !reflect.DeepEqual(stored.ContextCompressionOrder, want) {
		t.Fatalf("rejected API update mutated settings: view=%+v err=%v", stored, err)
	}
}

func TestAICompressionServerDiagnosticsPassSavedOrderAndBillActualUsageOnce(t *testing.T) {
	for _, mode := range []string{"compression", "ai_compression"} {
		t.Run(mode, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "server-compression-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			seedControllerAIModelPrice(t, db, "server-model", 1, 0.1, 2)
			disabled := false
			if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &disabled, nil); err != nil {
				t.Fatal(err)
			}
			order := []string{"local", "remote", "server"}
			if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{ContextCompressionEnabled: &disabled, ContextCompressionOrder: &order}); err != nil {
				t.Fatal(err)
			}
			before := service.GetAIContextCompressionSettingsWithDB(db)
			originalTool, originalStandalone, originalAI := newAIToolTestTool, runAIContextCompressionTest, runAIToolCompressionDebug
			t.Cleanup(func() {
				newAIToolTestTool, runAIContextCompressionTest, runAIToolCompressionDebug = originalTool, originalStandalone, originalAI
			})
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return &toolTestStub{} }
			calls := 0
			check := func(client *searchpkg.ChatGptClient, history, strategy string) {
				t.Helper()
				calls++
				if strategy != "server" || history != "historical facts to retain" || !client.ContextCompression.Enabled || !reflect.DeepEqual(client.ContextCompression.StrategyOrder, order) {
					t.Errorf("diagnostic lost explicit strategy or saved order: strategy=%q history=%q config=%+v", strategy, history, client.ContextCompression)
				}
			}
			usage := searchpkg.AISearchUsage{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020,
				ModelCalls: []searchpkg.AIModelCallUsage{{Model: "server-model", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}}}
			outcome := searchpkg.AIContextCompressionTestResult{Success: true, Strategy: "server", BeforeTokens: 3000, AfterTokens: 300, AttemptedStrategies: []string{"server"}}
			runAIContextCompressionTest = func(ctx context.Context, client *searchpkg.ChatGptClient, history, strategy string) (searchpkg.AIContextCompressionTestResult, searchpkg.AISearchDebugResult, error) {
				check(client, history, strategy)
				return outcome, searchpkg.AISearchDebugResult{Usage: usage}, nil
			}
			runAIToolCompressionDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, tool, target, history, strategy string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
				check(client, history, strategy)
				if tool != "web_search" || target != "OpenAI" {
					t.Errorf("incorrect AI diagnostic input: tool=%q target=%q", tool, target)
				}
				return [][]string{{"retained facts"}}, searchpkg.AISearchDebugResult{Usage: usage}, outcome, nil
			}
			response := callToolTest(t, user, `{"mode":"`+mode+`","tool":"web_search","target":"OpenAI","history":"historical facts to retain","compressionMode":"server","baseUrl":"https://example.com/v1","apiKey":"fixture","model":"server-model"}`)
			var result struct {
				Success       bool                                     `json:"success"`
				RequestID     string                                   `json:"requestId"`
				Usage         searchpkg.AISearchUsage                  `json:"usage"`
				Compression   searchpkg.AIContextCompressionTestResult `json:"compression"`
				BillingRecord service.BillingRecordListItem            `json:"billingRecord"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !result.Success || result.Compression.Strategy != "server" || calls != 1 || len(result.Usage.ModelCalls) != 1 || result.Usage.TotalTokens != 1020 {
				t.Fatalf("status=%d calls=%d response=%s", response.Code, calls, response.Body)
			}
			var records []entity.BillingRecord
			if err := db.Where("request_id = ?", result.RequestID).Find(&records).Error; err != nil {
				t.Fatal(err)
			}
			bill := result.BillingRecord
			if len(records) != 1 || records[0].UserID != user.ID || bill.Amount != 0.00095 || len(bill.FeeDetails) != 1 {
				t.Fatalf("server diagnostic duplicated or misassigned model usage: records=%+v bill=%+v", records, bill)
			}
			var account entity.User
			if err := db.First(&account, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if account.Balance != util.RoundMoney(10-bill.Amount) {
				t.Fatalf("server diagnostic deducted usage more than once: balance=%v bill=%+v", account.Balance, bill)
			}
			if after := service.GetAIContextCompressionSettingsWithDB(db); !reflect.DeepEqual(before, after) {
				t.Fatalf("explicit server diagnostic changed saved settings: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAICompressionOrderAndServerDiagnosticRemainAdminOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "compression-non-admin", "user")
	originalUpdate, originalStandalone, originalAI := updateAISearchConfig, runAIContextCompressionTest, runAIToolCompressionDebug
	t.Cleanup(func() {
		updateAISearchConfig, runAIContextCompressionTest, runAIToolCompressionDebug = originalUpdate, originalStandalone, originalAI
	})
	updateAISearchConfig = func(service.UpdateAISearchConfigInput) (service.AISearchConfigView, error) {
		t.Error("non-admin reached settings persistence")
		return service.AISearchConfigView{}, nil
	}
	runAIContextCompressionTest = func(context.Context, *searchpkg.ChatGptClient, string, string) (searchpkg.AIContextCompressionTestResult, searchpkg.AISearchDebugResult, error) {
		t.Error("non-admin reached standalone server diagnostic")
		return searchpkg.AIContextCompressionTestResult{}, searchpkg.AISearchDebugResult{}, nil
	}
	runAIToolCompressionDebug = func(context.Context, *searchpkg.ChatGptClient, string, string, string, string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
		t.Error("non-admin reached AI server diagnostic")
		return nil, searchpkg.AISearchDebugResult{}, searchpkg.AIContextCompressionTestResult{}, nil
	}
	for _, handler := range []gin.HandlerFunc{AISearchConfigDetail, UpdateAISearchConfig} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/ai-search-config", bytes.NewBufferString(`{"contextCompressionOrder":["local","remote","server"]}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", &user)
		handler(c)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("non-admin settings status=%d response=%s", recorder.Code, recorder.Body)
		}
	}
	for _, mode := range []string{"compression", "ai_compression"} {
		response := callToolTest(t, user, `{"mode":"`+mode+`","tool":"web_search","target":"OpenAI","history":"history","compressionMode":"server"}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s status=%d response=%s", mode, response.Code, response.Body)
		}
	}
	var records int64
	if err := db.Model(&entity.BillingRecord{}).Count(&records).Error; err != nil || records != 0 {
		t.Fatalf("forbidden diagnostics produced billing: records=%d err=%v", records, err)
	}
}
