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
		`{"mode":"direct","tool":"both","query":"test"}`,
		`{"mode":"ai","tool":"both","target":""}`,
		`{"mode":"ai","tool":"both","target":"https://example.com/#part"}`,
		`{"mode":"ai","tool":"web_read","target":"search words"}`,
		`{"mode":"ai","tool":"web_search","target":"file:///test"}`,
		`{"mode":"ai","tool":"both","target":"` + strings.Repeat("中", 121) + `"}`,
		`{"mode":"ai","tool":"both","target":"https://example.com/` + strings.Repeat("a", 2048) + `"}`,
		`{"mode":"ai","tool":"both","target":"http://127.0.0.1/"}`,
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

func TestAIToolCombinedRequiresWebReadBeforeModelCall(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "tool-admin", "admin")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	originalTool, originalDebug := newAIToolTestTool, runAIToolDebug
	t.Cleanup(func() { newAIToolTestTool, runAIToolDebug = originalTool, originalDebug })
	tool := &toolTestStub{}
	newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool {
		return struct{ searchpkg.AISearchTool }{tool}
	}
	called := false
	runAIToolDebug = func(context.Context, *searchpkg.ChatGptClient, string, string) ([][]string, searchpkg.AISearchDebugResult, error) {
		called = true
		return nil, searchpkg.AISearchDebugResult{}, nil
	}
	res := callToolTest(t, user, `{"mode":"ai","tool":"both","target":"test"}`)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "未配置网页读取工具") || called || tool.searches+tool.reads != 0 {
		t.Fatalf("status=%d called=%t tool=%+v response=%s", res.Code, called, tool, res.Body)
	}
}

func TestAIToolAIInputUsesTargetAndPreservesLegacyFields(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
	}{
		{"combined keyword", `{"mode":"ai","tool":"both","target":" tool keyword "}`, "tool keyword"},
		{"combined URL", `{"mode":"ai","tool":"both","target":"https://example.com/page"}`, "https://example.com/page"},
		{"target precedence", `{"mode":"ai","tool":"web_search","target":"chosen","query":"legacy"}`, "chosen"},
		{"legacy search", `{"mode":"ai","tool":"web_search","query":" legacy "}`, "legacy"},
		{"legacy read", `{"mode":"ai","tool":"web_read","url":"https://example.com/page"}`, "https://example.com/page"},
		{"legacy combined", `{"mode":"ai","tool":"both","query":"legacy","url":"https://example.com/page"}`, "https://example.com/page"},
		{"search long URL", `{"mode":"ai","tool":"web_search","target":"https://example.com/` + strings.Repeat("a", 121) + `"}`, "https://example.com/" + strings.Repeat("a", 121)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input aiToolTestInput
			if err := json.Unmarshal([]byte(test.body), &input); err != nil {
				t.Fatal(err)
			}
			target, err := input.validate()
			if err != nil || target != test.want {
				t.Fatalf("target=%q err=%v want=%q", target, err, test.want)
			}
		})
	}
}

func TestAIToolAIInputRejectsExplicitEmptyTarget(t *testing.T) {
	var input aiToolTestInput
	if err := json.Unmarshal([]byte(`{"mode":"ai","tool":"web_search","target":" ","query":"old keyword"}`), &input); err != nil {
		t.Fatal(err)
	}
	if target, err := input.validate(); err == nil {
		t.Fatalf("explicit empty target reused old input: %q", target)
	}
}

func TestAIToolCombinedModelCallsMergeBothToolCharges(t *testing.T) {
	t.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "true")
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("complete=%t", complete), func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "combined-tool-admin", "admin")
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
			rounds := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rounds++
				var request struct {
					ToolChoice any `json:"tool_choice"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if rounds == 1 && request.ToolChoice != "auto" {
					t.Errorf("AI test should use prompt-driven tool choice: %+v", request.ToolChoice)
				}
				output := []any{gin.H{"type": "message", "role": "assistant", "content": []any{gin.H{"type": "output_text", "text": `{"answers":["tool results received"]}`}}}}
				if rounds == 1 {
					output = []any{gin.H{"type": "function_call", "id": "fc_search", "call_id": "call_search", "name": "web_search", "arguments": `{"query":"tool keyword"}`, "status": "completed"}}
				} else if rounds == 2 && complete {
					output = []any{gin.H{"type": "function_call", "id": "fc_read", "call_id": "call_read", "name": "web_read", "arguments": `{"url":"https://example.com"}`, "status": "completed"}}
				}
				input, cached, out := rounds*1000, []int{0, 100, 1000, 1500}[rounds], rounds*20
				payload, _ := json.Marshal(gin.H{"type": "response.completed", "response": gin.H{"status": "completed", "model": "tool-model", "output": output,
					"usage": gin.H{"input_tokens": input, "input_tokens_details": gin.H{"cached_tokens": cached}, "output_tokens": out, "total_tokens": input + out}}})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
			}))
			defer server.Close()
			body, _ := json.Marshal(gin.H{"mode": "ai", "tool": "both", "target": "tool keyword", "baseUrl": server.URL, "apiKey": "sk-fixture", "model": "tool-model"})
			res := callToolTest(t, user, string(body))
			var response struct {
				Success       bool                          `json:"success"`
				TestPrompt    string                        `json:"testPrompt"`
				RequestID     string                        `json:"requestId"`
				BillingRecord service.BillingRecordListItem `json:"billingRecord"`
				Usage         searchpkg.AISearchUsage       `json:"usage"`
				ToolResult    []json.RawMessage             `json:"toolResult"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			wantRounds, wantReads, wantRecords, wantDetails := 3, 1, 3, 5
			wantAmount := 0.5039
			if !complete {
				wantRounds, wantReads, wantRecords, wantDetails, wantAmount = 2, 0, 2, 3, 0.20213
			}
			if res.Code != http.StatusOK || response.Success != complete || rounds != wantRounds || tool.searches != 1 || tool.reads != wantReads || len(response.ToolResult) != 1+wantReads {
				t.Fatalf("rounds=%d tool=%+v response=%s", rounds, tool, res.Body)
			}
			if !strings.Contains(response.TestPrompt, "tool keyword") || !strings.Contains(response.TestPrompt, "web_search") || !strings.Contains(response.TestPrompt, "web_read") {
				t.Fatalf("missing generated prompt: %q", response.TestPrompt)
			}
			bill := response.BillingRecord
			if bill.ID == 0 || bill.Amount != wantAmount || len(bill.FeeDetails) != wantDetails || len(response.Usage.ModelCalls) != wantRounds {
				t.Fatalf("incorrect combined bill: %+v usage=%+v", bill, response.Usage)
			}
			if complete && (bill.CacheHitRate == nil || *bill.CacheHitRate != float64(2600)/6000) {
				t.Fatalf("incorrect cache rate: %+v", bill)
			}
			var records []entity.BillingRecord
			db.Where("request_id = ?", response.RequestID).Find(&records)
			var account entity.User
			db.First(&account, user.ID)
			if len(records) != wantRecords || account.Balance != util.RoundMoney(10-wantAmount) {
				t.Fatalf("records=%+v balance=%f", records, account.Balance)
			}
		})
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
			var billingQueries []string
			if err := db.Callback().Query().After("gorm:query").Register("capture-tool-test-billing", func(tx *gorm.DB) {
				if sql := tx.Statement.SQL.String(); strings.Contains(sql, "billing_records") {
					billingQueries = append(billingQueries, sql)
				}
			}); err != nil {
				t.Fatal(err)
			}
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
			if len(billingQueries) == 0 {
				t.Fatal("billing response lookup was not captured")
			}
			for _, sql := range billingQueries {
				if strings.Contains(strings.ToUpper(sql), "INSTR(") || strings.Contains(sql, "matching_records") {
					t.Fatalf("test billing lookup must use exact owner and request filters instead of keyword scans: %s", sql)
				}
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
						_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"queued","message":"Task submitted"}`))
					} else if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/async-tool-test") {
						switch webPolls.Add(1) {
						case 1:
							_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"queued"}`))
						case 2:
							_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":"processing"}`))
						default:
							_, _ = w.Write([]byte(`{"task_id":"async-tool-test","status":" SuCcEeDeD ","result":{"markdown":"# Async test page"}}`))
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
			if toolName == "web_read" && (webSubmits.Load() != 1 || webPolls.Load() != 3 || !strings.Contains(string(response.ToolResult[0]), "Async test page")) {
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
			if toolName == "web_read" {
				var webReadBills int
				for _, record := range records {
					if record.Provider == "web_read" {
						webReadBills++
						if record.UserID != user.ID || record.Amount != 0.3 {
							t.Fatalf("incorrect completed web read charge: %+v", record)
						}
					}
				}
				if webReadBills != 1 {
					t.Fatalf("completed web read should be billed once: %+v", records)
				}
			}
			var reloaded entity.User
			db.First(&reloaded, user.ID)
			if len(records) != 2 || reloaded.Balance != util.RoundMoney(10-wantAmount) {
				t.Fatalf("records=%+v balance=%f", records, reloaded.Balance)
			}
		})
	}
}
