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

func TestAICompressionNoNetworkInputDoesNotReuseToolTargets(t *testing.T) {
	for _, body := range []string{
		`{"mode":"ai_compression","tool":"none","history":"historical facts"}`,
		`{"mode":"ai_compression","tool":"none","target":"","history":"historical facts"}`,
		`{"mode":"ai_compression","tool":"none","target":"old target","query":"old search","url":"https://example.com/old","history":"historical facts"}`,
	} {
		var input aiToolTestInput
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
		if target, err := input.validate(); err != nil || target != "" {
			t.Fatalf("history-only test must accept no target and ignore stale tool fields: target=%q err=%v", target, err)
		}
	}
	for _, body := range []string{
		`{"mode":"ai","tool":"none","target":"keyword"}`,
		`{"mode":"direct","tool":"none","query":"keyword"}`,
		`{"mode":"ai_compression","tool":"none","history":" "}`,
		`{"mode":"ai_compression","tool":"none","history":"history","compressionMode":"unknown"}`,
		`{"mode":"ai_compression","tool":"web_search","target":"","history":"history"}`,
		`{"mode":"ai_compression","tool":"web_read","target":"","history":"history"}`,
		`{"mode":"ai_compression","tool":"both","target":"","history":"history"}`,
	} {
		var input aiToolTestInput
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := input.validate(); err == nil {
			t.Fatalf("invalid or unintended tool test accepted: %s", body)
		}
	}
}

func TestAICompressionNoNetworkSkipsUAPIAndBillsActualModelUsageOnce(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, successful := range []bool{false, true} {
			t.Run(fmt.Sprintf("uapi=%t/success=%t", configured, successful), func(t *testing.T) {
				db := newSearchControllerTestDB(t)
				user := seedControllerUser(t, db, 0, "history-readback-admin", "admin")
				if err := db.Model(&user).Update("balance", 10).Error; err != nil {
					t.Fatal(err)
				}
				seedControllerAIModelPrice(t, db, "readback-model", 1, 0.1, 2)
				disabled := false
				if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &disabled, nil); err != nil {
					t.Fatal(err)
				}
				if configured {
					if _, err := service.UpdateSystemSettingDetailWithDB(db, service.UpdateSystemSettingInput{UAPIAPIKey: "fixture-uapi-key"}); err != nil {
						t.Fatal(err)
					}
				}
				originalTool, originalDebug := newAIToolTestTool, runAIToolCompressionDebug
				t.Cleanup(func() { newAIToolTestTool, runAIToolCompressionDebug = originalTool, originalDebug })
				toolCreations, modelCalls := 0, 0
				newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool {
					toolCreations++
					return &toolTestStub{}
				}
				runAIToolCompressionDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, tool, target, history, mode string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
					modelCalls++
					if tool != "none" || target != "" || history != "private history to read back" || mode != "configured" || client.SearchTool != nil {
						t.Errorf("history-only diagnostic unexpectedly retained tool/target: tool=%q target=%q history=%q mode=%q SearchTool=%T", tool, target, history, mode, client.SearchTool)
					}
					usage := searchpkg.AISearchUsage{InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020,
						ModelCalls: []searchpkg.AIModelCallUsage{{Model: "readback-model", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}}}
					var err error
					if !successful {
						err = errors.New("readback failed after model usage")
					}
					return [][]string{{"retained history facts"}}, searchpkg.AISearchDebugResult{Usage: usage}, searchpkg.AIContextCompressionTestResult{Success: true, Strategy: "local", Summary: "retained facts"}, err
				}
				response := callToolTest(t, user, `{"mode":"ai_compression","tool":"none","history":"private history to read back","compressionMode":"configured","baseUrl":"https://example.com/v1","apiKey":"fixture-ai-key","model":"readback-model"}`)
				var result struct {
					Success       bool                          `json:"success"`
					RequestID     string                        `json:"requestId"`
					Usage         searchpkg.AISearchUsage       `json:"usage"`
					ToolResult    []any                         `json:"toolResult"`
					BillingRecord service.BillingRecordListItem `json:"billingRecord"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || result.Success != successful || modelCalls != 1 || toolCreations != 0 || len(result.ToolResult) != 0 || len(result.Usage.ModelCalls) != 1 || len(result.Usage.SearchCalls) != 0 || len(result.Usage.WebReadCalls) != 0 {
					t.Fatalf("history-only dispatch or usage incorrect: status=%d modelCalls=%d toolCreations=%d response=%s", response.Code, modelCalls, toolCreations, response.Body)
				}
				bill := result.BillingRecord
				if bill.ID == 0 || bill.Amount != 0.00095 || bill.Subject != "AI 压缩历史回读测试" || len(bill.FeeDetails) != 1 {
					t.Fatalf("readback usage was not billed once with a meaningful project: %+v", bill)
				}
				var records []entity.BillingRecord
				if err := db.Where("request_id = ?", result.RequestID).Find(&records).Error; err != nil {
					t.Fatal(err)
				}
				if len(records) != 1 || records[0].UserID != user.ID || records[0].Provider != "ai" || strings.Contains(records[0].QuestionText, "private history") {
					t.Fatalf("readback created extra tool charges or exposed history: %+v", records)
				}
				var account entity.User
				if err := db.First(&account, user.ID).Error; err != nil {
					t.Fatal(err)
				}
				if account.Balance != util.RoundMoney(10-bill.Amount) {
					t.Fatalf("readback model usage deducted more than once: balance=%v bill=%+v", account.Balance, bill)
				}
			})
		}
	}
}

func TestAICompressionNoNetworkRemainsAdminOnly(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "history-readback-user", "user")
	originalTool, originalDebug := newAIToolTestTool, runAIToolCompressionDebug
	t.Cleanup(func() { newAIToolTestTool, runAIToolCompressionDebug = originalTool, originalDebug })
	newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool {
		t.Error("non-admin reached tool construction")
		return nil
	}
	runAIToolCompressionDebug = func(context.Context, *searchpkg.ChatGptClient, string, string, string, string) ([][]string, searchpkg.AISearchDebugResult, searchpkg.AIContextCompressionTestResult, error) {
		t.Error("non-admin reached history readback")
		return nil, searchpkg.AISearchDebugResult{}, searchpkg.AIContextCompressionTestResult{}, nil
	}
	response := callToolTest(t, user, `{"mode":"ai_compression","tool":"none","history":"history"}`)
	var records int64
	if err := db.Model(&entity.BillingRecord{}).Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusForbidden || records != 0 {
		t.Fatalf("non-admin readback produced usage: status=%d records=%d response=%s", response.Code, records, response.Body)
	}
}
