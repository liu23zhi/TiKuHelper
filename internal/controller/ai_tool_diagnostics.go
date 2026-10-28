package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type aiToolTestInput struct {
	aiSearchTestInput
	Mode   string  `json:"mode"`
	Tool   string  `json:"tool"`
	Target *string `json:"target"`
	Query  string  `json:"query"`
	URL    string  `json:"url"`
}

var newAIToolTestTool = service.BuildUAPISearchToolWithDB

var runAIToolDebug = func(ctx context.Context, client *search.ChatGptClient, tool, target string) ([][]string, search.AISearchDebugResult, error) {
	return client.TestToolCall(ctx, tool, target)
}

// recordedAITestTool captures the same result returned to the model, without a
// second external call or exposing the server-side UAPI credential.
type recordedAITestTool struct {
	tool    search.AISearchTool
	results []any
}

func (r *recordedAITestTool) SearchAggregate(ctx context.Context, request uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	result, err := r.tool.SearchAggregate(ctx, request)
	if err == nil && len(result.Results) == 0 {
		err = errors.New("搜索工具未返回搜索结果")
	}
	r.results = append(r.results, gin.H{"tool": "web_search", "query": request.Query, "result": result, "rawResponse": result.RawJSON, "error": toolTestError(err)})
	return result, err
}

func (r *recordedAITestTool) ReadWebPage(ctx context.Context, address string) (uapi.WebToMarkdownTask, error) {
	reader, ok := r.tool.(search.AIWebReadTool)
	if !ok {
		return uapi.WebToMarkdownTask{}, errors.New("未配置网页读取工具")
	}
	result, err := reader.ReadWebPage(ctx, address)
	if err == nil && (result.Status != "completed" || result.Result == nil || strings.TrimSpace(result.Result.Markdown) == "") {
		err = errors.New("网页读取工具未返回网页正文")
	}
	r.results = append(r.results, gin.H{"tool": "web_read", "url": address, "result": result, "rawResponse": result.RawJSON, "error": toolTestError(err)})
	return result, err
}

func toolTestError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (input *aiToolTestInput) validate() (string, error) {
	input.Mode, input.Tool = strings.TrimSpace(input.Mode), strings.TrimSpace(input.Tool)
	if input.Mode != "direct" && input.Mode != "ai" {
		return "", errors.New("测试类型须为 direct 或 ai")
	}
	if input.Mode == "ai" {
		target := ""
		if input.Target != nil {
			target = strings.TrimSpace(*input.Target)
		} else {
			switch input.Tool {
			case "web_read":
				target = strings.TrimSpace(input.URL)
			case "both":
				target = strings.TrimSpace(input.URL)
				if target == "" {
					target = strings.TrimSpace(input.Query)
				}
			default:
				target = strings.TrimSpace(input.Query)
			}
		}
		_, err := search.BuildAIToolTestPrompt(input.Tool, target)
		if err == nil && strings.Contains(target, "://") {
			target, err = uapi.ValidateWebURL(target)
		}
		return target, err
	}
	switch input.Tool {
	case "web_search":
		input.Query = strings.TrimSpace(input.Query)
		if count := len([]rune(input.Query)); count == 0 || count > 120 {
			return "", errors.New("搜索词须为 1 至 120 个字符")
		}
		return input.Query, nil
	case "web_read":
		if len([]rune(input.URL)) > 2048 {
			return "", errors.New("网页地址不能超过 2048 个字符")
		}
		return uapi.ValidateWebURL(input.URL)
	default:
		return "", errors.New("仅支持 web_search 和 web_read 测试")
	}
}

// TestAIToolCall uses the production token/tool billing functions, while bypassing
// question caches and question ingestion. All charges belong to the current admin.
func TestAIToolCall(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限测试 AI 工具调用"})
		return
	}
	var input aiToolTestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	target, err := input.validate()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "账单数据库不可用"})
		return
	}
	var account entity.User
	if err := db.First(&account, operator.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取测试账户失败"})
		return
	}
	if account.Balance <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "当前管理员余额不足，请充值后测试"})
		return
	}
	tool := newAIToolTestTool(db)
	if tool == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先在系统设置中配置有效的 UApiPro 令牌"})
		return
	}
	if _, ok := tool.(search.AIWebReadTool); (input.Tool == "web_read" || input.Tool == "both") && !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "未配置网页读取工具"})
		return
	}
	config := service.AISearchRequestConfig{}
	testPrompt := ""
	if input.Mode == "ai" {
		testPrompt, _ = search.BuildAIToolTestPrompt(input.Tool, target)
		query := url.Values{"url": {input.BaseURL}, "key": {input.APIKey}, "model": {input.Model}}
		resolved, err := service.BuildAISearchRequestConfigWithDB(db, query, true)
		if err != nil || !service.IsAISearchConfigUsable(resolved.Config) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "请先配置 AI 地址、密钥及测试模型"})
			return
		}
		config = resolved.Config
		if strings.TrimSpace(input.Prompt) != "" {
			config.Prompt = strings.TrimSpace(input.Prompt)
		}
	}

	clearAIResponseWriteDeadline(c)
	requestID := "tool-test-" + uuid.NewString()
	recorder := &recordedAITestTool{tool: tool}
	var answers [][]string
	var debug search.AISearchDebugResult
	if input.Mode == "direct" {
		debug, err = runDirectAIToolTest(c.Request.Context(), recorder, input.Tool, target)
	} else {
		client := &search.ChatGptClient{Enable: true, BaseURL: config.BaseURL, APIKey: config.APIKey, Model: config.Model,
			Prompt: config.Prompt, SearchTool: recorder, RequestID: requestID, MaxAISearchRetries: config.MaxAISearchRetries,
			ContextCompression: service.BuildAIContextCompressionConfigWithDB(db, config)}
		answers, debug, err = runAIToolDebug(c.Request.Context(), client, input.Tool, target)
	}
	secrets := []string{config.APIKey, service.GetUAPIAPIKeyWithDB(db)}
	// Sanitize before persisting tool errors as well as before returning diagnostics.
	encodedUsage, _ := json.Marshal(redactAIToolTestData(debug.Usage, secrets))
	_ = json.Unmarshal(encodedUsage, &debug.Usage)
	question := logger.RedactSecrets(service.BuildAIToolTestBillingQuestion(input.Mode, input.Tool, target), secrets...)
	req := model.SearchRequest{Question: question, DeepThinking: input.Mode == "ai", RequestID: requestID}
	resolved := resolvedSearchRequestContext{UserID: operator.ID, RequestID: requestID, SearchMode: searchModeAI,
		UsedDefaultAI: input.Mode == "ai", AIConfig: config}
	charges, warning, billingErr := resolveCompletedSearchBillingCharges(db, resolved, "ai", req, debug.Usage, false, len(answers) > 0)
	if billingErr == nil && len(charges) > 0 {
		billingErr = persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: operator.ID, RequestID: requestID,
			UsedDefaultAccount: true, Billable: true, DeepThinking: input.Mode == "ai", AIRoute: search.AISearchRouteDeepNative, ClientIP: c.ClientIP()}, charges)
	}
	var billingRecord *service.BillingRecordListItem
	if billingErr != nil {
		warning = strings.TrimSpace(warning + " 测试已执行，但账单保存失败，请检查服务日志。")
		logger.SysError(fmt.Sprintf("AI工具测试账单保存失败 requestId=%s: %s", requestID, logger.RedactSecrets(billingErr.Error(), secrets...)))
	} else if len(charges) > 0 {
		list, listErr := service.ListBillingRecordsWithDB(db, operator, service.BillingRecordListQuery{
			UserID: operator.ID, RequestID: requestID, Type: "normal_search",
		}, 1, 1)
		if listErr == nil && len(list.Data) > 0 {
			billingRecord = &list.Data[0]
		} else {
			warning = strings.TrimSpace(warning + " 账单已保存，详情读取失败，请按请求编号到“账单明细”查看。")
		}
	}
	flattened := make([]string, 0)
	for _, group := range answers {
		flattened = append(flattened, group...)
	}
	message := "工具调用成功并返回结果"
	if err != nil {
		message = "工具调用测试失败：" + err.Error()
	}
	c.JSON(http.StatusOK, redactAIToolTestData(gin.H{
		"success": err == nil, "message": message, "requestId": requestID, "answers": flattened,
		"testPrompt": testPrompt,
		"toolResult": recorder.results, "usage": debug.Usage, "trace": debug.Usage.Trace,
		"requestBody": debug.RequestBody, "rawResponse": debug.RawResponse, "endpoint": debug.Endpoint,
		"billingRecord": billingRecord, "billingWarning": warning,
	}, secrets))
}

func runDirectAIToolTest(ctx context.Context, recorder *recordedAITestTool, tool, target string) (search.AISearchDebugResult, error) {
	started := time.Now()
	debug := search.AISearchDebugResult{}
	var err error
	status := "success"
	if tool == "web_search" {
		result, callErr := recorder.SearchAggregate(ctx, uapi.SearchAggregateRequest{Query: target})
		err = callErr
		if err != nil {
			status = "error"
		}
		debug.Usage.SearchCalls = []search.AISearchCallUsage{{API: uapi.DefaultSearchAggregateURL, Query: target, Status: status,
			DurationMs: time.Since(started).Milliseconds(), ResultCount: len(result.Results), TotalResults: result.TotalResults, Error: toolTestError(err)}}
	} else {
		result, callErr := recorder.ReadWebPage(ctx, target)
		err = callErr
		if err != nil {
			status = "error"
		}
		markdownBytes := 0
		if result.Result != nil {
			markdownBytes = len(result.Result.Markdown)
		}
		debug.Usage.WebReadCalls = []search.AIWebReadCallUsage{{API: uapi.DefaultWebToMarkdownURL, URL: target, TaskID: result.TaskID, Status: status,
			DurationMs: time.Since(started).Milliseconds(), MarkdownBytes: markdownBytes, Error: toolTestError(err)}}
	}
	return debug, err
}

func redactAIToolTestData(input any, secrets []string) any {
	encoded, _ := json.Marshal(input)
	var value any
	_ = json.Unmarshal(encoded, &value)
	var redact func(any) any
	redact = func(value any) any {
		switch item := value.(type) {
		case string:
			return logger.RedactSecrets(item, secrets...)
		case map[string]any:
			for key, child := range item {
				item[key] = redact(child)
			}
		case []any:
			for index, child := range item {
				item[index] = redact(child)
			}
		}
		return value
	}
	return redact(value)
}
