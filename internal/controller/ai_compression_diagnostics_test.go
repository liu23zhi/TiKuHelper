package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestAICompressionInputValidation(t *testing.T) {
	for _, test := range []struct {
		body string
		ok   bool
	}{
		{`{"mode":"compression","history":"history"}`, true},
		{`{"mode":"compression","history":"history","compressionMode":"configured"}`, true},
		{`{"mode":"compression","history":"history","compressionMode":"remote"}`, true},
		{`{"mode":"compression","history":" "}`, false},
		{`{"mode":"compression","history":"history","compressionMode":"unknown"}`, false},
		{`{"mode":"ai_compression","tool":"both","target":"OpenAI","history":"history","compressionMode":"local"}`, true},
		{`{"mode":"ai_compression","tool":"both","target":"OpenAI"}`, false},
		{`{"mode":"ai_compression","tool":"web_read","target":"keyword","history":"history"}`, false},
		{`{"mode":"ai_compression","tool":"both","target":"OpenAI","history":"history","compressionMode":"unknown"}`, false},
	} {
		var input aiToolTestInput
		if err := json.Unmarshal([]byte(test.body), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := input.validate(); (err == nil) != test.ok {
			t.Errorf("body=%s err=%v", test.body, err)
		}
	}
}

func TestAIStandaloneCompressionSkipsToolsAndBillsEveryActualModelCall(t *testing.T) {
	for _, successful := range []bool{true, false} {
		t.Run(fmt.Sprint(successful), func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "compression-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			seedControllerAIModelPrice(t, db, "compression-model", 1, 0.1, 2)
			disabled := false
			if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &disabled, nil); err != nil {
				t.Fatal(err)
			}
			originalTool, originalCompression := newAIToolTestTool, runAIContextCompressionTest
			t.Cleanup(func() { newAIToolTestTool, runAIContextCompressionTest = originalTool, originalCompression })
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool {
				t.Error("standalone compression unnecessarily requires UAPI")
				return nil
			}
			runAIContextCompressionTest = func(ctx context.Context, client *searchpkg.ChatGptClient, history, mode string) (searchpkg.AIContextCompressionTestResult, searchpkg.AISearchDebugResult, error) {
				if history != "test-only private historical text" || mode != "remote" || client.Model != "compression-model" || client.SearchTool != nil || client.RequestID == "" || client.Prompt != "custom test prompt" {
					t.Errorf("incorrect diagnostic arguments history=%q mode=%q client=%+v", history, mode, client)
				}
				usage := searchpkg.AISearchUsage{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020,
					ModelCalls: []searchpkg.AIModelCallUsage{{Model: "compression-model", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}}}
				var err error
				if !successful {
					err = errors.New("summary failed diagnostic-private-key")
				}
				return searchpkg.AIContextCompressionTestResult{Success: successful, Strategy: "local", BeforeTokens: 2000, AfterTokens: 1000, Summary: "summary diagnostic-private-key", RemoteFallback: true}, searchpkg.AISearchDebugResult{Usage: usage}, err
			}
			res := callToolTest(t, user, `{"mode":"compression","history":"test-only private historical text","compressionMode":"remote","baseUrl":"https://example.com/v1","apiKey":"diagnostic-private-key","model":"compression-model","prompt":"custom test prompt"}`)
			var response struct {
				Success       bool                                     `json:"success"`
				Compression   searchpkg.AIContextCompressionTestResult `json:"compression"`
				BillingRecord service.BillingRecordListItem            `json:"billingRecord"`
				RequestID     string                                   `json:"requestId"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if res.Code != http.StatusOK || response.Success != successful || response.Compression.Success != successful || strings.Contains(res.Body.String(), "diagnostic-private-key") {
				t.Fatalf("status=%d response=%s", res.Code, res.Body)
			}
			bill := response.BillingRecord
			if bill.ID == 0 || bill.Amount != 0.00095 || bill.Subject != "AI 历史压缩测试：历史上下文" || bill.CacheHitRate == nil || *bill.CacheHitRate != 0.1 || len(bill.FeeDetails) != 1 {
				t.Fatalf("standalone compression usage was not billed: %+v", bill)
			}
			var account entity.User
			db.First(&account, user.ID)
			if account.Balance != util.RoundMoney(10-bill.Amount) {
				t.Fatalf("balance=%v bill=%+v", account.Balance, bill)
			}
			var records []entity.BillingRecord
			db.Where("request_id = ?", response.RequestID).Find(&records)
			if len(records) != 1 || records[0].UserID != user.ID || strings.Contains(records[0].QuestionText, "private historical") {
				t.Fatalf("wrong account or leaked historical context in billing project: %+v", records)
			}
		})
	}
}

func TestAIToolCompressionControllerDispatchesAndBillsFailureAfterCompression(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "compression-tool-admin", "admin")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	seedControllerAIModelPrice(t, db, "compression-model", 1, 0.1, 2)
	originalTool, originalCompression := newAIToolTestTool, runAIToolCompressionDebug
	t.Cleanup(func() { newAIToolTestTool, runAIToolCompressionDebug = originalTool, originalCompression })
	newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return &toolTestStub{} }
	called := false
	runAIToolCompressionDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, tool, target, history, mode string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
		called = true
		if tool != "both" || target != "OpenAI" || history != "synthetic history" || mode != "local" || client.SearchTool == nil {
			t.Errorf("wrong tool-compression dispatch %q %q %q %q", tool, target, history, mode)
		}
		return nil, searchpkg.AISearchDebugResult{Usage: searchpkg.AISearchUsage{InputTokens: 1000, OutputTokens: 20, TotalTokens: 1020,
				ModelCalls: []searchpkg.AIModelCallUsage{{Model: "compression-model", InputTokens: 1000, OutputTokens: 20, TotalTokens: 1020}}}},
			searchpkg.AIContextCompressionTestResult{Success: true, Strategy: "local", BeforeTokens: 2000, AfterTokens: 1000, Summary: "test summary"}, errors.New("tool loop failed after successful compression")
	}
	res := callToolTest(t, user, `{"mode":"ai_compression","tool":"both","target":"OpenAI","history":"synthetic history","compressionMode":"local","baseUrl":"https://example.com/v1","apiKey":"fixture","model":"compression-model"}`)
	var response struct {
		Success       bool                                     `json:"success"`
		Compression   searchpkg.AIContextCompressionTestResult `json:"compression"`
		BillingRecord service.BillingRecordListItem            `json:"billingRecord"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || !called || response.Success || !response.Compression.Success || response.BillingRecord.ID == 0 || !strings.Contains(response.BillingRecord.Subject, "AI 工具调用压缩测试") || !strings.Contains(response.BillingRecord.Subject, "上下文压缩 + 联网搜索 + 网页浏览") {
		t.Fatalf("status=%d called=%t response=%s", res.Code, called, res.Body)
	}
}

func TestAICompressionDiagnosticsForceWindowLookupWithoutChangingSavedSettings(t *testing.T) {
	for _, mode := range []string{"compression", "ai_compression"} {
		t.Run(mode, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "disabled-compression-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			disabled := false
			if _, err := service.UpdateAISearchConfigWithDB(db, service.UpdateAISearchConfigInput{ContextCompressionEnabled: &disabled}); err != nil {
				t.Fatal(err)
			}
			window := int64(32000)
			if _, err := service.CreateAIModelPriceWithDB(db, service.CreateAIModelPriceInput{ModelName: "window-test-model", ContextWindowTokens: &window}); err != nil {
				t.Fatal(err)
			}
			before := service.GetAIContextCompressionSettingsWithDB(db)
			if before.Enabled {
				t.Fatal("fixture must disable saved automatic compression")
			}
			originalTool, originalStandalone, originalAI := newAIToolTestTool, runAIContextCompressionTest, runAIToolCompressionDebug
			t.Cleanup(func() {
				newAIToolTestTool, runAIContextCompressionTest, runAIToolCompressionDebug = originalTool, originalStandalone, originalAI
			})
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return &toolTestStub{} }
			called := false
			checkClient := func(client *searchpkg.ChatGptClient) {
				called = true
				if !client.ContextCompression.Enabled || client.ContextCompression.ContextWindowTokens != window {
					t.Errorf("explicit test must resolve the model window with saved auto compression off: %+v", client.ContextCompression)
				}
			}
			runAIContextCompressionTest = func(ctx context.Context, client *searchpkg.ChatGptClient, history, strategy string) (searchpkg.AIContextCompressionTestResult, searchpkg.AISearchDebugResult, error) {
				checkClient(client)
				return searchpkg.AIContextCompressionTestResult{Success: true}, searchpkg.AISearchDebugResult{}, nil
			}
			runAIToolCompressionDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, tool, target, history, strategy string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
				checkClient(client)
				return [][]string{{"test"}}, searchpkg.AISearchDebugResult{}, searchpkg.AIContextCompressionTestResult{Success: true}, nil
			}
			res := callToolTest(t, user, `{"mode":"`+mode+`","tool":"web_search","target":"OpenAI","history":"synthetic history","compressionMode":"local","baseUrl":"https://example.com/v1","apiKey":"fixture","model":"window-test-model"}`)
			if res.Code != http.StatusOK || !called || !strings.Contains(res.Body.String(), `"success":true`) {
				t.Fatalf("status=%d called=%t response=%s", res.Code, called, res.Body)
			}
			if after := service.GetAIContextCompressionSettingsWithDB(db); after != before {
				t.Fatalf("one-off diagnostic changed saved compression settings: before=%+v after=%+v", before, after)
			}
		})
	}
}
