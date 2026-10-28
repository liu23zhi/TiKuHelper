package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type toolTestStub struct {
	searches, reads int
	err             error
}

func (s *toolTestStub) SearchAggregate(context.Context, uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	s.searches++
	return uapi.SearchAggregateResponse{Results: []uapi.SearchResult{{Title: "test", URL: "https://example.com"}}}, s.err
}

func (s *toolTestStub) ReadWebPage(context.Context, string) (uapi.WebToMarkdownTask, error) {
	s.reads++
	return uapi.WebToMarkdownTask{Status: "completed", TaskID: "task-1", Result: &uapi.WebToMarkdownResult{Markdown: "# Test page"}}, s.err
}

func callToolTest(t *testing.T, user entity.User, payload string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/ai-search-config/tool-test", bytes.NewBufferString(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &user)
	TestAIToolCall(c)
	return recorder
}

func TestAIToolTestValidatesBeforeExternalCalls(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "tool-admin", "admin")
	_ = db.Model(&user).Update("balance", 10).Error
	original := newAIToolTestTool
	t.Cleanup(func() { newAIToolTestTool = original })
	tool := &toolTestStub{}
	newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return tool }
	for _, body := range []string{
		`{"mode":"bad","tool":"web_search","query":"test"}`,
		`{"mode":"direct","tool":"unknown","query":"test"}`,
		`{"mode":"direct","tool":"web_search","query":""}`,
		`{"mode":"direct","tool":"web_search","query":"` + strings.Repeat("中", 121) + `"}`,
		`{"mode":"direct","tool":"web_read","url":"file:///test"}`,
	} {
		if res := callToolTest(t, user, body); res.Code != http.StatusBadRequest {
			t.Fatalf("invalid request: %d %s", res.Code, res.Body)
		}
	}
	user.Role = "user"
	if res := callToolTest(t, user, `{"mode":"direct","tool":"web_search","query":"test"}`); res.Code != http.StatusForbidden {
		t.Fatalf("non-admin: %d %s", res.Code, res.Body)
	}
	if tool.searches+tool.reads != 0 {
		t.Fatal("invalid request reached tool")
	}
}

func TestAIToolDirectTestPersistsRealToolCharges(t *testing.T) {
	for _, toolName := range []string{"web_search", "web_read"} {
		t.Run(toolName, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "tool-admin", "admin")
			_ = db.Model(&user).Update("balance", 10).Error
			_ = service.SetAISearchPricePerCallWithDB(db, 0.2)
			_ = service.SetWebReadPricePerCallWithDB(db, 0.3)
			original := newAIToolTestTool
			t.Cleanup(func() { newAIToolTestTool = original })
			tool := &toolTestStub{}
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return tool }
			res := callToolTest(t, user, `{"mode":"direct","tool":"`+toolName+`","query":"test","url":"https://example.com/"}`)
			if res.Code != http.StatusOK {
				t.Fatalf("%d %s", res.Code, res.Body)
			}
			var response struct {
				Success       bool                          `json:"success"`
				RequestID     string                        `json:"requestId"`
				BillingRecord service.BillingRecordListItem `json:"billingRecord"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !response.Success || response.RequestID == "" || response.BillingRecord.ID == 0 {
				t.Fatalf("%s", res.Body)
			}
			var records []entity.BillingRecord
			db.Find(&records)
			if len(records) != 1 || records[0].UserID != user.ID || records[0].RequestID != response.RequestID {
				t.Fatalf("records: %+v", records)
			}
			want := 0.2
			if toolName == "web_read" {
				want = 0.3
			}
			if response.BillingRecord.Amount != want || len(response.BillingRecord.FeeDetails) != 1 {
				t.Fatalf("record: %+v", response.BillingRecord)
			}
			var reloaded entity.User
			db.First(&reloaded, user.ID)
			if reloaded.Balance != 10-want {
				t.Fatalf("balance: %f", reloaded.Balance)
			}
		})
	}
}

func TestAIToolTestFailureKeepsUsageAndAdminCallCacheRates(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "tool-admin", "admin")
	_ = db.Model(&user).Update("balance", 10).Error
	originalTool, originalDebug := newAIToolTestTool, runAIToolDebug
	t.Cleanup(func() { newAIToolTestTool, runAIToolDebug = originalTool, originalDebug })
	newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return &toolTestStub{} }
	runAIToolDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, tool, target string) ([][]string, searchpkg.AISearchDebugResult, error) {
		return nil, searchpkg.AISearchDebugResult{RawResponse: `{"error":"custom-private-value"}`, Usage: searchpkg.AISearchUsage{
			InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020,
			ModelCalls:   []searchpkg.AIModelCallUsage{{Model: "gpt-test", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 20, TotalTokens: 1020}},
			WebReadCalls: []searchpkg.AIWebReadCallUsage{{URL: "https://example.com", Status: "error"}},
		}}, errors.New("tool failed custom-private-value")
	}
	res := callToolTest(t, user, `{"mode":"ai","tool":"web_read","url":"https://example.com/","baseUrl":"https://example.com/v1","apiKey":"custom-private-value","model":"gpt-test"}`)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "custom-private-value") {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	var response struct {
		Success       bool                          `json:"success"`
		BillingRecord service.BillingRecordListItem `json:"billingRecord"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &response)
	if response.Success || response.BillingRecord.ID == 0 || response.BillingRecord.CacheHitRate == nil || *response.BillingRecord.CacheHitRate != 0.1 {
		t.Fatalf("%s", res.Body)
	}
	if len(response.BillingRecord.FeeDetails) != 1 || response.BillingRecord.FeeDetails[0].CacheHitRate == nil {
		t.Fatalf("call details: %+v", response.BillingRecord.FeeDetails)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("provider = ?", "web_read").Count(&count)
	if count != 0 {
		t.Fatal("failed web read was billed")
	}
}

func TestAIToolModelAndToolSuccessMergeActualUsageIntoAdminBill(t *testing.T) {
	t.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "true")
	for _, toolName := range []string{"web_search", "web_read"} {
		t.Run(toolName, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "tool-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			seedControllerAIModelPrice(t, db, "tool-model", 1, 0.1, 2)
			disabled := false
			if err := service.SetAIStagedTokenBillingSettingsWithDB(db, &disabled, nil); err != nil {
				t.Fatal(err)
			}
			if err := service.SetAISearchPricePerCallWithDB(db, 0.2); err != nil {
				t.Fatal(err)
			}
			if err := service.SetWebReadPricePerCallWithDB(db, 0.3); err != nil {
				t.Fatal(err)
			}
			original := newAIToolTestTool
			t.Cleanup(func() { newAIToolTestTool = original })
			tool := &toolTestStub{}
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return tool }
			var webSubmits, webPolls atomic.Int32
			if toolName == "web_read" {
				webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var billed int64
					db.Model(&entity.BillingRecord{}).Where("provider = ?", "web_read").Count(&billed)
					if billed != 0 {
						t.Error("async web read was billed before the completed result")
					}
					if r.Method == http.MethodPost {
						webSubmits.Add(1)
						w.WriteHeader(http.StatusAccepted)
						_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"pending"}`))
					} else if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/async-tool-test") {
						if webPolls.Add(1) == 1 {
							_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"processing"}`))
						} else {
							_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"completed","result":{"markdown":"# Async test page"}}`))
						}
					} else {
						t.Errorf("unexpected async tool request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer webServer.Close()
				newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool {
					return uapi.NewClient("uapi-fixture").SetWebToMarkdownURL(webServer.URL + "/async").SetMaxRetries(0)
				}
			}
			rounds := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rounds++
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
					t.Errorf("unexpected model request: %s %s", r.Method, r.URL.Path)
				}
				var output []any
				input, cached, tokens := 1000, 100, 20
				if rounds == 1 {
					args := `{"query":"test"}`
					if toolName == "web_read" {
						args = `{"url":"https://example.com/"}`
					}
					output = []any{gin.H{"type": "function_call", "id": "fc_test", "call_id": "call_test", "name": toolName, "arguments": args, "status": "completed"}}
				} else {
					input, cached, tokens = 9000, 4500, 180
					output = []any{gin.H{"type": "message", "role": "assistant", "content": []any{gin.H{"type": "output_text", "text": `{"answers":["tool result received"]}`}}}}
				}
				payload, _ := json.Marshal(gin.H{"type": "response.completed", "response": gin.H{"status": "completed", "model": "tool-model", "output": output,
					"usage": gin.H{"input_tokens": input, "input_tokens_details": gin.H{"cached_tokens": cached}, "output_tokens": tokens, "total_tokens": input + tokens}}})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
			}))
			defer server.Close()
			body, _ := json.Marshal(gin.H{"mode": "ai", "tool": toolName, "query": "test", "url": "https://example.com/", "baseUrl": server.URL, "apiKey": "sk-fixture", "model": "tool-model"})
			res := callToolTest(t, user, string(body))
			var response struct {
				Success       bool                          `json:"success"`
				RequestID     string                        `json:"requestId"`
				BillingRecord service.BillingRecordListItem `json:"billingRecord"`
				Usage         searchpkg.AISearchUsage       `json:"usage"`
				ToolResult    []json.RawMessage             `json:"toolResult"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if res.Code != http.StatusOK || !response.Success || rounds != 2 || tool.searches+tool.reads+int(webSubmits.Load()) != 1 || len(response.ToolResult) != 1 {
				t.Fatalf("rounds=%d tool=%+v response=%s", rounds, tool, res.Body)
			}
			if toolName == "web_read" && (webSubmits.Load() != 1 || webPolls.Load() != 2 || !strings.Contains(string(response.ToolResult[0]), "Async test page")) {
				t.Fatalf("async web read did not wait for its body: submissions=%d polls=%d result=%s", webSubmits.Load(), webPolls.Load(), response.ToolResult)
			}
			bill := response.BillingRecord
			wantAmount := 0.20626
			if toolName == "web_read" {
				wantAmount = 0.30626
			}
			if bill.ID == 0 || bill.Amount != wantAmount || bill.CacheHitRate == nil || *bill.CacheHitRate != 0.46 || len(bill.FeeDetails) != 3 {
				t.Fatalf("incorrect merged admin bill: %+v", bill)
			}
			rates := []float64{}
			for _, detail := range bill.FeeDetails {
				if detail.CacheHitRate != nil {
					rates = append(rates, *detail.CacheHitRate)
				}
			}
			if len(rates) != 2 || rates[0] != 0.1 || rates[1] != 0.5 || response.Usage.InputTokens != 10000 || response.Usage.CachedInputTokens != 4600 {
				t.Fatalf("per-call cache rates=%v usage=%+v", rates, response.Usage)
			}
			var records []entity.BillingRecord
			db.Where("request_id = ?", response.RequestID).Find(&records)
			var reloaded entity.User
			db.First(&reloaded, user.ID)
			if len(records) != 2 || reloaded.Balance != util.RoundMoney(10-wantAmount) {
				t.Fatalf("records=%+v balance=%f", records, reloaded.Balance)
			}
		})
	}
}
