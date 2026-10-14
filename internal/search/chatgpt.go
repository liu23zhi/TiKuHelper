package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-resty/resty/v2"
	"github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// ChatGptClient AI 搜题客户端
type ChatGptClient struct {
	Enable                bool
	APIKey                string
	BaseURL               string
	Model                 string
	Prompt                string
	Temperature           float64
	TopP                  float64
	SearchTool            AISearchTool
	RequestID             string
	MaxAISearchModelCalls int
	MaxAISearches         int
}

type AISearchUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	TotalTokens       int64
	SearchCalls       []AISearchCallUsage `json:"searchCalls,omitempty"`
	Trace             []AISearchTrace     `json:"trace,omitempty"`
}

type AISearchCallUsage struct {
	API           string `json:"api"`
	Query         string `json:"query"`
	Status        string `json:"status"`
	DurationMs    int64  `json:"durationMs"`
	ResultCount   int    `json:"resultCount"`
	TotalResults  int    `json:"totalResults"`
	SourceDetails string `json:"sourceDetails,omitempty"`
	Error         string `json:"error,omitempty"`
}

type AISearchDebugResult struct {
	Endpoint    string
	RequestBody string
	RawResponse string
	Usage       AISearchUsage
	Trace       []AISearchTrace `json:"trace,omitempty"`
}

// AISearchTrace records the observable request/tool path. Full model output and
// raw stream data are written to the dedicated thinking log and omitted from the
// structured audit payload to keep database records bounded.
type AISearchTrace struct {
	Round               int    `json:"round"`
	Stage               string `json:"stage"`
	Model               string `json:"model"`
	Endpoint            string `json:"endpoint"`
	Query               string `json:"query,omitempty"`
	SearchAPI           string `json:"searchApi,omitempty"`
	SourceDetails       string `json:"sourceDetails,omitempty"`
	ResultSummary       string `json:"resultSummary,omitempty"`
	Status              string `json:"status,omitempty"`
	DurationMs          int64  `json:"durationMs,omitempty"`
	ModelCallCount      int    `json:"modelCallCount,omitempty"`
	ModelCallBudget     int    `json:"modelCallBudget,omitempty"`
	RemainingModelCalls int    `json:"remainingModelCalls"`
	SearchCount         int    `json:"searchCount,omitempty"`
	SearchBudget        int    `json:"searchBudget,omitempty"`
	RemainingSearches   int    `json:"remainingSearches"`
	ResultCount         int    `json:"resultCount,omitempty"`
	CacheHit            bool   `json:"cacheHit,omitempty"`
	ReasoningChars      int    `json:"reasoningChars,omitempty"`
	ContentPreview      string `json:"contentPreview,omitempty"`
	Error               string `json:"error,omitempty"`
	RequestBody         string `json:"-"`
	FullResponse        string `json:"-"`
	RawResponse         string `json:"-"`
}

func (in *ChatGptClient) prepareAISearchTrace(trace AISearchTrace) AISearchTrace {
	if strings.TrimSpace(trace.Model) == "" {
		trace.Model = strings.TrimSpace(in.Model)
	}
	trace.Endpoint = logger.RedactSecrets(trace.Endpoint, in.APIKey)
	trace.SearchAPI = logger.RedactSecrets(trace.SearchAPI, in.APIKey)
	trace.Query = truncateAIToolText(trace.Query, 120)
	trace.SourceDetails = truncateAIToolText(logger.RedactSecrets(trace.SourceDetails, in.APIKey), 1200)
	trace.ResultSummary = truncateAIToolText(logger.RedactSecrets(trace.ResultSummary, in.APIKey), 1200)
	trace.ContentPreview = truncateAIToolText(logger.RedactSecrets(trace.ContentPreview, in.APIKey), 240)
	trace.Error = logger.RedactSecrets(trace.Error, in.APIKey)
	return trace
}

func (in *ChatGptClient) appendAISearchTrace(usage *AISearchUsage, trace AISearchTrace) {
	structuredTrace := in.prepareAISearchTrace(trace)
	if usage != nil {
		usage.Trace = append(usage.Trace, structuredTrace)
	}
	in.logAISearchTrace(trace)
}

func (in *ChatGptClient) logAISearchTrace(trace AISearchTrace) {
	structuredTrace := in.prepareAISearchTrace(trace)
	requestID := strings.TrimSpace(in.RequestID)
	logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜索轨迹 requestId=%s round=%d stage=%s model=%s endpoint=%s searchAPI=%s query=%s status=%s durationMs=%d modelCalls=%d/%d remainingModelCalls=%d searches=%d/%d remainingSearches=%d results=%d cacheHit=%t sources=%s resultSummary=%s reasoningChars=%d contentPreview=%s error=%s", requestID, structuredTrace.Round, structuredTrace.Stage, strings.TrimSpace(structuredTrace.Model), structuredTrace.Endpoint, structuredTrace.SearchAPI, structuredTrace.Query, structuredTrace.Status, structuredTrace.DurationMs, structuredTrace.ModelCallCount, structuredTrace.ModelCallBudget, structuredTrace.RemainingModelCalls, structuredTrace.SearchCount, structuredTrace.SearchBudget, structuredTrace.RemainingSearches, structuredTrace.ResultCount, structuredTrace.CacheHit, structuredTrace.SourceDetails, structuredTrace.ResultSummary, structuredTrace.ReasoningChars, structuredTrace.ContentPreview, structuredTrace.Error), in.APIKey)
	if trace.SourceDetails != "" {
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜索完整来源 requestId=%s round=%d stage=%s\n%s", requestID, trace.Round, trace.Stage, trace.SourceDetails), in.APIKey)
	}
	if trace.ResultSummary != "" {
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜索完整结果摘要 requestId=%s round=%d stage=%s\n%s", requestID, trace.Round, trace.Stage, trace.ResultSummary), in.APIKey)
	}
	if trace.RequestBody != "" {
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI模型完整请求 requestId=%s round=%d stage=%s\n%s", requestID, trace.Round, trace.Stage, trace.RequestBody), in.APIKey)
	}
	if trace.FullResponse != "" {
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI模型完整响应 requestId=%s round=%d stage=%s\n%s", requestID, trace.Round, trace.Stage, trace.FullResponse), in.APIKey)
	}
	if trace.RawResponse != "" {
		label := "AI模型原始流响应"
		if trace.SearchAPI != "" {
			label = "AI搜索API完整响应"
		}
		logger.AIThinkingWithSecrets(fmt.Sprintf("%s requestId=%s round=%d stage=%s\n%s", label, requestID, trace.Round, trace.Stage, trace.RawResponse), in.APIKey)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type openAIChatCompletionRequest struct {
	Model         string               `json:"model"`
	Stream        bool                 `json:"stream"`
	Temperature   float64              `json:"temperature"`
	TopP          float64              `json:"top_p"`
	Messages      []openAIChatMessage  `json:"messages"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
	Tools         []openAITool         `json:"tools,omitempty"`
	ToolChoice    interface{}          `json:"tool_choice,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIContentPart struct {
	Type     string              `json:"type"`
	Text     string              `json:"text,omitempty"`
	ImageURL *openAIImageURLPart `json:"image_url,omitempty"`
}

type openAIImageURLPart struct {
	URL string `json:"url"`
}

type openAIStreamChunk struct {
	Error   json.RawMessage `json:"error,omitempty"`
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content,omitempty"`
			ToolCalls        []openAIToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIStreamUsage `json:"usage,omitempty"`
}

type openAIStreamUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CacheCreation *struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation,omitempty"`
}

type aiSearchAnswerPayload struct {
	Answers []string `json:"answers"`
}

var aiSearchURLPattern = regexp.MustCompile(`https?://[A-Za-z0-9][A-Za-z0-9._~:/?@!$&'()*+,;=%-]*`)

const maxAISearchAttachmentBytes = 8 * 1024 * 1024
const aiSearchAttachmentTimeout = 60 * time.Second
const aiSearchFirstTokenTimeout = 5 * time.Minute

// 题库公式截图常见尺寸只有 40x22 左右，直接提交给模型时上下标几乎不可辨认，这里统一放大。
const (
	aiSearchImageMagnifyBelowWidth  = 320
	aiSearchImageMagnifyBelowHeight = 160
	aiSearchImageTargetWidth        = 1000
	aiSearchImageTargetHeight       = 500
	aiSearchImageMaxScale           = 8
)

// 多人同时搜题时的并发控制：
//   - aiSearchMaxConcurrentRequests 限制同时在跑的上游 AI 请求数，避免打爆上游额度或连接池；
//   - aiSearchMaxConcurrentAttachments 限制单个请求内的图片并发下载数；
//   - aiSearchRetryMaxAttempts / aiSearchRetryBaseDelay 处理上游 429、5xx 和网络抖动。
const (
	aiSearchMaxConcurrentRequests    = 8
	aiSearchMaxConcurrentAttachments = 4
	aiSearchRetryMaxAttempts         = 3
	aiSearchRetryBaseDelay           = 400 * time.Millisecond
	aiSearchRetryMaxDelay            = 5 * time.Second
	aiSearchRetryBudget              = 2 * time.Minute
)

var aiSearchRequestSemaphore = newAISearchSemaphore(aiSearchMaxConcurrentRequests)

// newAISearchSemaphore 创建一个带缓冲通道实现的计数信号量。
func newAISearchSemaphore(limit int) chan struct{} {
	if limit <= 0 {
		limit = 1
	}
	return make(chan struct{}, limit)
}

func acquireAISearchSlot(ctx context.Context, semaphore chan struct{}) error {
	if semaphore == nil {
		return nil
	}
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseAISearchSlot(semaphore chan struct{}) {
	if semaphore == nil {
		return
	}
	select {
	case <-semaphore:
	default:
	}
}

var errAISearchFirstTokenTimeout = fmt.Errorf("AI 首字等待超时")

// DefaultAISearchPrompt 返回内置的系统提示词。
// 这是所有题型（单选、多选、填空、判断、简答、阅读理解、大作业）共用的唯一副本。
func DefaultAISearchPrompt() string {
	return strings.TrimSpace(`
你是一个用于题库检索结果补充的答题助手，你的作答会被直接交给正在用题库搜题的学生。
请只返回 JSON，不要输出 markdown、代码块或额外解释。
输出格式固定为：{"answers":["答案1","答案2"]}。

【作答身份】
- 以答题者本人的第一人称作答，语气像一名认真、踏实的学生，而不是旁观者或AI助手。
- 绝对不要用“不知道”“不确定”“无法确定”“建议你自己思考”“请咨询老师”等推脱式回答。
- 只要题干提供了信息，就必须给出一个明确、可直接提交的答案，不要反问用户，不要要求补充信息。
- 遇到“你能不能……”“你是否愿意……”这类表态题或自我要求题，直接给出正面、具体、可执行的承诺（例如说明打算怎么做、用什么方式记录、多久复盘一次），不要回答“不知道”。
- 涉及价值观、行为规范、学习计划类题目时，用真诚、具体、可落地的表述，不要空喊口号，也不要过度夸张。

【通用要求】
- 先读全题干、选项和图片，再作答；题干中的编号、小问、括号说明都必须覆盖。
- **一定要给出真正回答了问题的内容**，而不是把题干里的材料换个说法复述一遍。
  如果题干本身包含一段材料、草稿或示例文本，请把它当作线索去回答"问题"，
  不要把那段材料当成答案原样抄回来。
- 若发现"题目"读起来像是一段答案、草稿或考生已填的内容（例如整段第一人称陈述），
  说明取到的文本可能有偏差：请据此推断**真正被问的是什么**并作答；
  确实无法推断时，给出与这段内容最匹配的完整、可直接提交的作答，但必须比原文本更完整、更具体。
- 计算题、证明题、信号与系统、电路、数学等题目必须写出推理过程与最终结果：写出所用公式或变换、关键中间步骤，最后给出结论。
- 答案中出现的公式用纯文本表示，例如 H(z)=(1+(1/3)z^-1)/(1-(3/4)z^-1+(1/8)z^-2)、y(n)=...；不要使用 LaTeX 的 \( \) 或 $ $ 包裹。
- 保持答案稳定、简洁，并兼容题库系统后续处理。

【分题型规则】
- 单选题：只返回 1 个最可能答案，优先返回与某个选项完全一致的文本。
- 多选题：按最终答案顺序返回多个答案文本，不要遗漏应选项。
- 判断题：只返回 1 个判断结果；有选项时优先返回与选项完全一致的文本，没有选项时只返回“正确”或“错误”。
- 填空题：按题干中空的出现顺序，每个空返回一个答案；不要合并、跳过或调换空的顺序。
- 连线题、匹配题：按每组对应关系依次返回。
- 简答题、问答题、阅读理解、大作业：必须回答全部小问；按题目原编号组织答案，保留必要的推导、公式、依据和结论。可以把完整作答放在一个 answers 元素中，也可以按小问拆分多个元素，但不得只回答其中一问。

【图片题】
- 题目里的图片通常是公式或图表的截图，请把它当作题干的一部分来读，不要因为看到 URL 或空白就放弃作答。
- 图片按出现顺序编号，用户提示末尾的“图X为”给出了每张图对应的链接；结合图号与题干文字理解题意。
- 如果图片确实是空白、损坏或与题目无关，也要基于题干文字、课程信息和学科常识给出最可能的答案，仍然不要返回“不确定”。
- 只有在题目、选项和图片全部缺失、完全无法判断题意时，才返回：{"answers":["不确定"]}。
	`)
}

func effectiveAISearchPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return DefaultAISearchPrompt()
	}
	return trimmed
}

// getHTTPClient returns an HTTP client without a response deadline for the AI stream.
func (in *ChatGptClient) getHTTPClient() *resty.Client {
	client := util.NewSafeHTTPClient(0)
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.ResponseHeaderTimeout = aiSearchFirstTokenTimeout
	}
	return resty.NewWithClient(client)
}

// getAttachmentHTTPClient keeps image downloads bounded independently of AI generation.
func (in *ChatGptClient) getAttachmentHTTPClient() *resty.Client {
	return resty.NewWithClient(util.NewSafeHTTPClient(aiSearchAttachmentTimeout))
}

// SearchAnswer 使用 OpenAI 兼容流式接口聚合答案
func (in *ChatGptClient) SearchAnswer(req model.SearchRequest) (answer [][]string, err error) {
	answer, _, err = in.SearchAnswerWithUsage(req)
	return answer, err
}

func (in *ChatGptClient) SearchAnswerDebug(req model.SearchRequest) (answer [][]string, debug AISearchDebugResult, err error) {
	if req.DeepThinking && in.SearchTool != nil {
		return in.searchAnswerWithTools(context.Background(), req)
	}
	answer = make([][]string, 0)
	if !in.Enable {
		return answer, debug, nil
	}

	baseURL := strings.TrimSpace(in.BaseURL)
	apiKey := strings.TrimSpace(in.APIKey)
	modelName := strings.TrimSpace(in.Model)
	if strings.TrimSpace(req.Question) == "" || baseURL == "" || apiKey == "" || modelName == "" {
		return answer, debug, nil
	}

	systemPrompt := effectiveAISearchPrompt(in.Prompt)
	userPromptParts := buildAISearchPromptParts(req, in.getAttachmentHTTPClient().GetClient())
	endpoints := buildAIChatCompletionEndpoints(baseURL)
	maxModelCalls, _ := in.aiSearchBudgets()
	modelCalls := 0
	var lastRequestErr error
	var allTrace []AISearchTrace
	for _, endpoint := range endpoints {
		if modelCalls >= maxModelCalls {
			break
		}
		content, usage, statusCode, requestBody, rawResponse, callErr := in.requestStreamAnswerDetailed(endpoint, systemPrompt, userPromptParts, &modelCalls, maxModelCalls)
		allTrace = append(allTrace, usage.Trace...)
		usage.Trace = append([]AISearchTrace(nil), allTrace...)
		debug = AISearchDebugResult{
			Endpoint:    endpoint,
			RequestBody: requestBody,
			RawResponse: rawResponse,
			Usage:       usage,
			Trace:       append([]AISearchTrace(nil), usage.Trace...),
		}
		if callErr == nil {
			lastRequestErr = nil
			parsedAnswers := parseAISearchAnswers(content, req.Options, req.Type)
			if len(parsedAnswers) > 0 {
				return [][]string{parsedAnswers}, debug, nil
			}
			continue
		}
		if isAISearchEndpointFallbackStatus(statusCode) {
			lastRequestErr = callErr
			continue
		}
		return nil, debug, callErr
	}
	if lastRequestErr != nil {
		return nil, debug, lastRequestErr
	}

	return nil, debug, errors.ErrTargetNoAnswer
}

func (in *ChatGptClient) SearchAnswerWithUsage(req model.SearchRequest) (answer [][]string, usage AISearchUsage, err error) {
	if req.DeepThinking && in.SearchTool != nil {
		answer, debug, toolErr := in.searchAnswerWithTools(context.Background(), req)
		return answer, debug.Usage, toolErr
	}
	if req.DeepThinking {
		logger.SysLog(fmt.Sprintf("AI 搜索降级 requestId=%s 原因=未配置联网搜索工具 行为=保持旧版一次性模型回复", strings.TrimSpace(req.RequestID)))
	}
	answer = make([][]string, 0)
	if !in.Enable {
		return answer, usage, nil
	}

	baseURL := strings.TrimSpace(in.BaseURL)
	apiKey := strings.TrimSpace(in.APIKey)
	modelName := strings.TrimSpace(in.Model)
	if strings.TrimSpace(req.Question) == "" || baseURL == "" || apiKey == "" || modelName == "" {
		return answer, usage, nil
	}

	systemPrompt := effectiveAISearchPrompt(in.Prompt)
	userPromptParts := buildAISearchPromptParts(req, in.getAttachmentHTTPClient().GetClient())
	endpoints := buildAIChatCompletionEndpoints(baseURL)
	maxModelCalls, _ := in.aiSearchBudgets()
	modelCalls := 0
	var lastRequestErr error
	var lastUsage AISearchUsage
	var allTrace []AISearchTrace
	for _, endpoint := range endpoints {
		if modelCalls >= maxModelCalls {
			break
		}
		content, streamUsage, statusCode, _, _, callErr := in.requestStreamAnswerDetailed(endpoint, systemPrompt, userPromptParts, &modelCalls, maxModelCalls)
		allTrace = append(allTrace, streamUsage.Trace...)
		streamUsage.Trace = append([]AISearchTrace(nil), allTrace...)
		lastUsage = streamUsage
		if callErr == nil {
			lastRequestErr = nil
			parsedAnswers := parseAISearchAnswers(content, req.Options, req.Type)
			if len(parsedAnswers) > 0 {
				return [][]string{parsedAnswers}, streamUsage, nil
			}
			// 上游只回了「不确定」这类无效答案：换一个端点重试往往能拿到有效答案，
			// 而不是直接把失败透传给用户。
			continue
		}
		if isAISearchEndpointFallbackStatus(statusCode) {
			lastRequestErr = callErr
			continue
		}
		return nil, streamUsage, callErr
	}
	if lastRequestErr != nil {
		return nil, lastUsage, lastRequestErr
	}

	return nil, lastUsage, errors.ErrTargetNoAnswer
}

func buildAISearchPrompt(req model.SearchRequest) string {
	var builder strings.Builder
	builder.WriteString("请根据系统规则返回答案 JSON。")
	builder.WriteString("\n题型：")
	builder.WriteString(resolveAISearchQuestionTypeLabel(req.Type))
	builder.WriteString("\n题目：")
	builder.WriteString(strings.TrimSpace(req.Question))
	switch req.Type {
	case 2:
		builder.WriteString("\n题型补充说明：该题型在调用侧同时承载填空题、连线题、匹配题；如果题目要求建立对应关系，请按每组对应关系依次返回 answers 数组。")
	case -4:
		builder.WriteString("\n题型补充说明：这是大作业或综合作答题，请给出可以整篇提交的完整作答，包含必要的推导、步骤与结论。")
	case 15:
		builder.WriteString("\n题型补充说明：这是阅读理解题，请先读懂材料再逐问作答，每问都要给出明确结论。")
	case 4:
		builder.WriteString("\n题型补充说明：该题型在调用侧可能是简答题，也可能是阅读理解或其他文本作答题；请直接返回覆盖全部小问的最终文本答案。")
	}
	if strings.TrimSpace(req.CourseName) != "" {
		builder.WriteString("\n课程：")
		builder.WriteString(strings.TrimSpace(req.CourseName))
	}
	if strings.TrimSpace(req.Extra) != "" {
		builder.WriteString("\n补充信息：")
		builder.WriteString(strings.TrimSpace(req.Extra))
	}
	if len(req.Options) > 0 {
		// 先净化再入 prompt：抓页面带进来的编辑器脚本与工具栏文字会挤掉真实选项、
		// 让模型分心甚至放弃作答（实测线上出现「模型把考生草稿当题干照抄」）。
		// 净化是保守的：不确定就保留原文，且绝不允许把选项清空。
		options := util.SanitizeOptionsForAI(req.Options)
		if len(options) > 0 {
			builder.WriteString("\n选项：")
			for idx, option := range util.FormatOptions(options, req.Type) {
				builder.WriteString(fmt.Sprintf("\n%c. %s", 'A'+idx, strings.TrimSpace(option)))
			}
		}
	}
	builder.WriteString("\n作答要求：如果题目包含多个小问，请按原编号完整回答每一问，不要只回答其中一问或随机选择一个小问。")
	builder.WriteString("\n作答要求：不要返回“不确定”“不知道”或“无法确定”；请综合题干、选项、图片、课程与学科常识给出最可能的完整答案。")
	return builder.String()
}

func resolveAISearchQuestionTypeLabel(questionType int) string {
	switch questionType {
	case 0:
		return "单选题"
	case 1:
		return "多选题"
	case 2:
		return "填空题/连线题"
	case 3:
		return "判断题"
	case 4:
		return "问答题/简答题/阅读理解题"
	case -4:
		return "问答题/大作业"
	case 15:
		return "阅读理解"
	default:
		return fmt.Sprintf("其他题型(%d)", questionType)
	}
}

func buildAISearchPromptParts(req model.SearchRequest, httpClient *http.Client) []openAIContentPart {
	parts := []openAIContentPart{{
		Type: "text",
		Text: buildAISearchPrompt(req),
	}}
	attachmentURLs := extractAISearchURLs(req)
	parts = append(parts, downloadAISearchAttachmentParts(httpClient, attachmentURLs)...)
	if len(attachmentURLs) > 0 {
		var references strings.Builder
		references.WriteString("图片对应关系（图片按出现顺序编号，请结合图号理解题目）：")
		for index, attachmentURL := range attachmentURLs {
			fmt.Fprintf(&references, "\n图%s为：%s", formatAISearchImageIndex(index+1), attachmentURL)
		}
		parts = append(parts, openAIContentPart{Type: "text", Text: references.String()})
	}
	return parts
}

// downloadAISearchAttachmentParts 并发下载题目配图，同时保持原有顺序，
// 避免多张公式图串行下载把单次搜题的耗时叠加到几十秒。
func downloadAISearchAttachmentParts(httpClient *http.Client, attachmentURLs []string) []openAIContentPart {
	if httpClient == nil || len(attachmentURLs) == 0 {
		return nil
	}

	type attachmentResult struct {
		index int
		part  openAIContentPart
		ok    bool
	}

	semaphore := make(chan struct{}, aiSearchMaxConcurrentAttachments)
	results := make(chan attachmentResult, len(attachmentURLs))
	var wg sync.WaitGroup

	for index, attachmentURL := range attachmentURLs {
		wg.Add(1)
		go func(index int, attachmentURL string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			part, ok := downloadAISearchAttachmentPart(httpClient, attachmentURL)
			results <- attachmentResult{index: index, part: part, ok: ok}
		}(index, attachmentURL)
	}

	wg.Wait()
	close(results)

	ordered := make([]attachmentResult, len(attachmentURLs))
	for result := range results {
		ordered[result.index] = result
	}

	parts := make([]openAIContentPart, 0, len(attachmentURLs))
	for _, result := range ordered {
		if !result.ok {
			continue
		}
		parts = append(parts, result.part)
	}
	return parts
}

func formatAISearchImageIndex(index int) string {
	if index >= 1 && index <= 10 {
		return []string{"一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}[index-1]
	}
	return fmt.Sprintf("%d", index)
}

func extractAISearchURLs(req model.SearchRequest) []string {
	seen := make(map[string]struct{})
	urls := make([]string, 0)
	appendMatches := func(value string) {
		for _, match := range aiSearchURLPattern.FindAllString(value, -1) {
			trimmed := strings.TrimSpace(strings.TrimRight(match, ".,;:!?)]}，。；：！？》」』、"))
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			urls = append(urls, trimmed)
		}
	}
	appendMatches(req.Question)
	for _, option := range req.Options {
		appendMatches(option)
	}
	return urls
}

func downloadAISearchAttachmentPart(httpClient *http.Client, attachmentURL string) (openAIContentPart, bool) {
	if httpClient == nil {
		return openAIContentPart{}, false
	}
	parsed, err := url.Parse(strings.TrimSpace(attachmentURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return openAIContentPart{}, false
	}
	httpReq, err := http.NewRequest(http.MethodGet, attachmentURL, nil)
	if err != nil {
		return openAIContentPart{}, false
	}
	httpReq.Header.Set("Referer", "https://mooc1.chaoxing.com/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return openAIContentPart{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return openAIContentPart{}, false
	}
	if resp.ContentLength > maxAISearchAttachmentBytes {
		return openAIContentPart{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAISearchAttachmentBytes+1))
	if err != nil || len(body) == 0 {
		return openAIContentPart{}, false
	}
	if len(body) > maxAISearchAttachmentBytes {
		return openAIContentPart{}, false
	}

	contentType := resolveAISearchAttachmentContentType(resp.Header.Values("Content-Type"), body)
	switch {
	case strings.HasPrefix(contentType, "image/"):
		preparedBody, preparedContentType := prepareAISearchImageAttachment(body, contentType)
		return openAIContentPart{
			Type: "image_url",
			ImageURL: &openAIImageURLPart{
				URL: "data:" + preparedContentType + ";base64," + base64.StdEncoding.EncodeToString(preparedBody),
			},
		}, true
	case contentType == "text/html":
		return openAIContentPart{
			Type: "text",
			Text: buildAISearchHTMLAttachment(parsed, body),
		}, true
	default:
		return openAIContentPart{}, false
	}
}

// prepareAISearchImageAttachment 规范化题目配图后再提交给多模态模型。
//
// 题库（例如超星）返回的公式截图通常是「透明背景 + 黑色字形」的小图。
// 这类图片一旦被上游按黑底解码，就会变成「黑字黑底」的全黑图，
// 模型只能看到一张空图，于是统一回答“不确定”——这正是公式题答不出来的根因。
// 因此这里统一把透明像素合成到白底，并适度放大过小的公式图。
func prepareAISearchImageAttachment(body []byte, contentType string) ([]byte, string) {
	decoded, format, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return body, contentType
	}

	prepared := flattenAISearchImageOntoWhite(decoded)
	if prepared == nil {
		return body, contentType
	}
	prepared = upscaleAISearchImage(prepared)

	var buf bytes.Buffer
	if err := png.Encode(&buf, prepared); err != nil {
		return body, contentType
	}
	if buf.Len() == 0 || buf.Len() > maxAISearchAttachmentBytes {
		return body, contentType
	}
	// 统一输出 PNG：无损、保留字形的锐利边缘，避免 JPEG 压缩糊掉下标和分数。
	_ = format
	return buf.Bytes(), "image/png"
}

// flattenAISearchImageOntoWhite 把带透明通道的图片合成到纯白背景上。
func flattenAISearchImageOntoWhite(src image.Image) image.Image {
	if src == nil {
		return nil
	}

	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	// 对每个目标像素合成：out = src*a + white*(1-a)
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			r, g, b, a := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if a == 0xFFFF {
				dst.SetRGBA(x, y, color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 0xFF})
				continue
			}
			// RGBA() 返回的是 alpha 预乘值，先还原再合成。
			unpremultipliedR, unpremultipliedG, unpremultipliedB := unpremultiplyRGBA(r, g, b, a)
			alpha := float64(a) / 65535
			blendedR := uint8(math.Round(float64(unpremultipliedR)*alpha + 255*(1-alpha)))
			blendedG := uint8(math.Round(float64(unpremultipliedG)*alpha + 255*(1-alpha)))
			blendedB := uint8(math.Round(float64(unpremultipliedB)*alpha + 255*(1-alpha)))
			dst.SetRGBA(x, y, color.RGBA{R: blendedR, G: blendedG, B: blendedB, A: 0xFF})
		}
	}
	return dst
}

func unpremultiplyRGBA(r uint32, g uint32, b uint32, a uint32) (uint32, uint32, uint32) {
	if a == 0 {
		return 0, 0, 0
	}
	if a == 0xFFFF {
		return r, g, b
	}
	return r * 0xFFFF / a, g * 0xFFFF / a, b * 0xFFFF / a
}

// upscaleAISearchImage 放大过小的公式图，提升模型对小字号上下标的识别率。
// 只放大、不缩小；整数倍最近邻插值可保持笔画边缘锐利。
func upscaleAISearchImage(src image.Image) image.Image {
	if src == nil {
		return nil
	}

	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	if width >= aiSearchImageMagnifyBelowWidth || height >= aiSearchImageMagnifyBelowHeight {
		return src
	}

	scale := 1
	for scale < aiSearchImageMaxScale {
		next := scale + 1
		if width*next > aiSearchImageTargetWidth || height*next > aiSearchImageTargetHeight {
			break
		}
		scale = next
	}
	if scale <= 1 {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, width*scale, height*scale))
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			dst.Set(x, y, src.At(bounds.Min.X+x/scale, bounds.Min.Y+y/scale))
		}
	}
	return dst
}

func resolveAISearchAttachmentContentType(headerValues []string, body []byte) string {
	for _, headerValue := range headerValues {
		contentType := normalizeAISearchContentType(headerValue)
		if strings.HasPrefix(contentType, "image/") || contentType == "text/html" {
			return contentType
		}
	}

	detected := normalizeAISearchContentType(http.DetectContentType(body))
	if strings.HasPrefix(detected, "image/") || detected == "text/html" || looksLikeHTML(body) {
		if detected == "text/plain" && looksLikeHTML(body) {
			return "text/html"
		}
		return detected
	}
	return ""
}

func normalizeAISearchContentType(value string) string {
	contentType := strings.ToLower(strings.TrimSpace(value))
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	switch contentType {
	case "application/xhtml+xml":
		return "text/html"
	default:
		return contentType
	}
}

func looksLikeHTML(body []byte) bool {
	prefix := strings.ToLower(strings.TrimSpace(string(body)))
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	return strings.HasPrefix(prefix, "<!doctype html") ||
		strings.HasPrefix(prefix, "<html") ||
		strings.HasPrefix(prefix, "<?xml") && strings.Contains(prefix, "<html")
}

func buildAISearchHTMLAttachment(parsedURL *url.URL, body []byte) string {
	filename := "attachment.html"
	if parsedURL != nil {
		filename = path.Base(strings.TrimSpace(parsedURL.Path))
		if filename == "." || filename == "/" || filename == "" {
			filename = "attachment"
		}
		if path.Ext(filename) == "" {
			filename += ".html"
		}
	}
	filename = strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(filename)
	return fmt.Sprintf(
		"<attached_file filename=\"%s\" content_type=\"text/html\">\n%s\n</attached_file>",
		filename,
		string(body),
	)
}

func buildAIChatCompletionEndpoints(baseURL string) []string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		return nil
	}

	endpoints := []string{normalized + "/chat/completions"}
	if !strings.HasSuffix(normalized, "/v1") {
		endpoints = append(endpoints, normalized+"/v1/chat/completions")
	}
	return endpoints
}

func (in *ChatGptClient) requestStreamAnswerDetailed(endpoint string, systemPrompt string, userPromptParts []openAIContentPart, modelCalls *int, maxModelCalls int) (string, AISearchUsage, int, string, string, error) {
	requestStartedAt := time.Now()

	var (
		requestBody     string
		lastUsage       AISearchUsage
		lastStatusCode  int
		lastRawResponse string
		lastErr         error = errors.ErrTargetServerError
		traceEvents     []AISearchTrace
	)
	recordTrace := func(trace AISearchTrace) {
		traceEvents = append(traceEvents, in.prepareAISearchTrace(trace))
		in.logAISearchTrace(trace)
	}

	for attempt := 0; attempt < aiSearchRetryMaxAttempts && *modelCalls < maxModelCalls; attempt++ {
		if attempt > 0 {
			if !sleepAISearchRetryBackoff(attempt, time.Since(requestStartedAt)) {
				break
			}
		}

		remainingCalls := maxModelCalls - *modelCalls - 1
		budgetPrompt := buildAISearchBudgetPrompt(remainingCalls, 0, remainingCalls == 0)
		payload, err := json.Marshal(openAIChatCompletionRequest{
			Model:       strings.TrimSpace(in.Model),
			Stream:      true,
			Temperature: 0,
			TopP:        0,
			Messages: []openAIChatMessage{
				{Role: "system", Content: systemPrompt + "\n" + budgetPrompt + "\n本轮未提供联网搜索工具，必须直接输出最终答案 JSON。"},
				{Role: "user", Content: userPromptParts},
			},
			StreamOptions: &openAIStreamOptions{IncludeUsage: true},
		})
		if err != nil {
			return "", AISearchUsage{}, 0, "", "", errors.ErrParserJSON
		}
		requestBody = string(payload)
		*modelCalls++
		attemptStartedAt := time.Now()
		remainingAfterCall := maxModelCalls - *modelCalls
		recordTrace(AISearchTrace{Round: *modelCalls, Stage: "普通模型调用开始", Endpoint: endpoint, ModelCallCount: *modelCalls, ModelCallBudget: maxModelCalls, RemainingModelCalls: remainingAfterCall})
		content, usage, statusCode, rawResponse, retryable, callErr := in.executeAISearchStreamRequest(endpoint, payload, requestBody, requestStartedAt)
		recordTrace(AISearchTrace{Round: *modelCalls, Stage: "普通模型响应", Endpoint: endpoint, ModelCallCount: *modelCalls, ModelCallBudget: maxModelCalls, RemainingModelCalls: maxModelCalls - *modelCalls, Status: fmt.Sprintf("%d", statusCode), DurationMs: time.Since(attemptStartedAt).Milliseconds(), RequestBody: requestBody, ContentPreview: content, FullResponse: content, RawResponse: rawResponse, Error: errorString(callErr)})
		usage.Trace = append([]AISearchTrace(nil), traceEvents...)
		lastUsage = usage
		lastStatusCode = statusCode
		lastRawResponse = rawResponse
		lastErr = callErr
		if callErr == nil || !retryable {
			return content, lastUsage, lastStatusCode, requestBody, lastRawResponse, callErr
		}
	}

	return "", lastUsage, lastStatusCode, requestBody, lastRawResponse, lastErr
}

// executeAISearchStreamRequest 在全局并发配额内执行单次上游请求。
// retryable 表示该失败是否值得退避重试（限流、网关错误、网络抖动）。
func (in *ChatGptClient) executeAISearchStreamRequest(endpoint string, payload []byte, requestBody string, requestStartedAt time.Time) (string, AISearchUsage, int, string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), aiSearchFirstTokenTimeout)
	defer cancel()

	if err := acquireAISearchSlot(ctx, aiSearchRequestSemaphore); err != nil {
		return "", AISearchUsage{}, 0, "", false, in.upstreamError("queue", 0, "", err)
	}
	defer releaseAISearchSlot(aiSearchRequestSemaphore)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", AISearchUsage{}, 0, "", false, in.upstreamError("request", 0, "", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(in.APIKey))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := in.getHTTPClient().GetClient().Do(httpReq)
	if err != nil {
		// 连接失败、超时、被重置通常都是瞬时故障，交给上层退避重试。
		return "", AISearchUsage{}, 0, "", true, in.upstreamError("network", 0, "", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		rawResponse := string(rawBody)
		return "", AISearchUsage{}, resp.StatusCode, rawResponse, isAISearchRetryableStatus(resp.StatusCode), in.upstreamError("http", resp.StatusCode, rawResponse, readErr)
	}

	firstTokenTimeout := aiSearchFirstTokenTimeout - time.Since(requestStartedAt)
	if firstTokenTimeout <= 0 {
		return "", AISearchUsage{}, resp.StatusCode, "", false, in.upstreamError("first_token", resp.StatusCode, "", errAISearchFirstTokenTimeout)
	}
	content, usage, rawResponse, err := readOpenAIStreamContentWithFirstTokenTimeout(resp.Body, firstTokenTimeout)
	if err != nil {
		return "", usage, resp.StatusCode, rawResponse, false, in.upstreamError("stream", resp.StatusCode, rawResponse, err)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		// 流里没有任何内容，可能是上游被中断：值得重试一次。
		return "", usage, resp.StatusCode, rawResponse, true, errors.ErrTargetNoAnswer
	}
	return content, usage, resp.StatusCode, rawResponse, false, nil
}

// isAISearchRetryableStatus 判断上游状态码是否值得退避重试。
func isAISearchRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// isAISearchEndpointFallbackStatus 判断是否应该换成备用端点再试一次。
func isAISearchEndpointFallbackStatus(statusCode int) bool {
	return statusCode == http.StatusNotFound || isAISearchRetryableStatus(statusCode)
}

// sleepAISearchRetryBackoff 指数退避，并受总重试预算约束。
// 返回 false 表示预算已经用尽，不应继续重试。
func sleepAISearchRetryBackoff(attempt int, elapsed time.Duration) bool {
	delay := time.Duration(float64(aiSearchRetryBaseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > aiSearchRetryMaxDelay {
		delay = aiSearchRetryMaxDelay
	}
	if elapsed+delay > aiSearchRetryBudget {
		return false
	}
	time.Sleep(delay)
	return true
}

func readOpenAIStreamContent(body io.Reader) (string, AISearchUsage, error) {
	content, usage, _, err := readOpenAIStreamContentWithRaw(body, nil)
	return content, usage, err
}

type openAIStreamReadResult struct {
	content string
	usage   AISearchUsage
	raw     string
	err     error
}

func readOpenAIStreamContentWithFirstTokenTimeout(body io.ReadCloser, timeout time.Duration) (string, AISearchUsage, string, error) {
	firstToken := make(chan struct{}, 1)
	result := make(chan openAIStreamReadResult, 1)
	go func() {
		content, usage, raw, err := readOpenAIStreamContentWithRaw(body, firstToken)
		result <- openAIStreamReadResult{content: content, usage: usage, raw: raw, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case readResult := <-result:
		return readResult.content, readResult.usage, readResult.raw, readResult.err
	case <-firstToken:
		readResult := <-result
		return readResult.content, readResult.usage, readResult.raw, readResult.err
	case <-timer.C:
		_ = body.Close()
		readResult := <-result
		return readResult.content, readResult.usage, readResult.raw, errAISearchFirstTokenTimeout
	}
}

func readOpenAIStreamContentWithRaw(body io.Reader, firstToken chan<- struct{}) (string, AISearchUsage, string, error) {
	scanner := bufio.NewScanner(body)
	var builder strings.Builder
	var raw strings.Builder
	var usage AISearchUsage
	for scanner.Scan() {
		raw.WriteString(scanner.Text())
		raw.WriteByte('\n')
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", AISearchUsage{}, raw.String(), err
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return "", usage, raw.String(), fmt.Errorf("AI upstream error event: %s", chunk.Error)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" && firstToken != nil {
				select {
				case firstToken <- struct{}{}:
				default:
				}
			}
			builder.WriteString(choice.Delta.Content)
		}
		if chunk.Usage != nil {
			usage = buildAISearchUsageFromStream(chunk.Usage)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", AISearchUsage{}, raw.String(), err
	}
	return builder.String(), usage, raw.String(), nil
}

func buildAISearchUsageFromStream(streamUsage *openAIStreamUsage) AISearchUsage {
	if streamUsage == nil {
		return AISearchUsage{}
	}

	usage := AISearchUsage{
		InputTokens:  streamUsage.PromptTokens,
		OutputTokens: streamUsage.CompletionTokens,
		TotalTokens:  streamUsage.TotalTokens,
	}
	if streamUsage.PromptTokensDetails != nil {
		usage.CachedInputTokens = normalizeAISearchCachedTokens(streamUsage.PromptTokensDetails.CachedTokens, streamUsage.PromptTokens)
	}
	if streamUsage.CacheCreation != nil {
		usage.CacheWriteTokens = normalizeAISearchCacheWriteTokens(
			streamUsage.CacheCreation.Ephemeral5mInputTokens+streamUsage.CacheCreation.Ephemeral1hInputTokens,
			streamUsage.PromptTokens,
		)
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

// normalizeAISearchCachedTokens 保证缓存 token 数量不超过总输入 token 数量，
// 避免上游返回异常数据时把缓存价格用出负成本。
func normalizeAISearchCachedTokens(cachedTokens int64, promptTokens int64) int64 {
	if cachedTokens <= 0 {
		return 0
	}
	if promptTokens > 0 && cachedTokens > promptTokens {
		return promptTokens
	}
	return cachedTokens
}

// normalizeAISearchCacheWriteTokens 同样把缓存写入 token 收敛到输入总量以内。
func normalizeAISearchCacheWriteTokens(cacheWriteTokens int64, promptTokens int64) int64 {
	if cacheWriteTokens <= 0 {
		return 0
	}
	if promptTokens > 0 && cacheWriteTokens > promptTokens {
		return promptTokens
	}
	return cacheWriteTokens
}

func parseAISearchAnswers(content string, options []string, questionType int) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	var payload aiSearchAnswerPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		return normalizeAISearchAnswers(payload.Answers, options, questionType)
	}

	var jsonArray []string
	if err := json.Unmarshal([]byte(trimmed), &jsonArray); err == nil {
		return normalizeAISearchAnswers(jsonArray, options, questionType)
	}

	return normalizeAISearchAnswers([]string{trimmed}, options, questionType)
}

func normalizeAISearchAnswers(rawAnswers []string, options []string, questionType int) []string {
	seen := make(map[string]struct{}, len(rawAnswers))
	answers := make([]string, 0, len(rawAnswers))
	// 非客观题（填空题/连线题/简答题/阅读理解）必须保留重复答案：
	// 多个空或多个连线点可能是同一个答案文本，去重会让答案数与空数对不上。
	preserveRepeatedAnswers := util.AllowsRepeatedAnswers(questionType)
	for _, rawAnswer := range rawAnswers {
		resolved := resolveAISearchAnswer(rawAnswer, options)
		if resolved == "" {
			continue
		}
		if !preserveRepeatedAnswers {
			if _, ok := seen[resolved]; ok {
				continue
			}
			seen[resolved] = struct{}{}
		}
		answers = append(answers, resolved)
	}
	return answers
}

func resolveAISearchAnswer(answer string, options []string) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return ""
	}

	if mapped := matchAISearchOptionByLabel(trimmed, options); mapped != "" {
		return mapped
	}
	if mapped := matchAISearchJudgementOption(trimmed, options); mapped != "" {
		return mapped
	}
	if mapped := matchAISearchOptionByText(trimmed, options); mapped != "" {
		return mapped
	}

	return trimmed
}

func matchAISearchOptionByLabel(answer string, options []string) string {
	index := extractAISearchOptionIndex(answer)
	if index < 0 || index >= len(options) {
		return ""
	}
	return strings.TrimSpace(options[index])
}

func matchAISearchOptionByText(answer string, options []string) string {
	for _, option := range options {
		trimmedOption := strings.TrimSpace(option)
		if trimmedOption == "" {
			continue
		}
		if strings.EqualFold(answer, trimmedOption) {
			return trimmedOption
		}
	}
	return ""
}

func matchAISearchJudgementOption(answer string, options []string) string {
	polarity, ok := aiSearchJudgementPolarity(answer)
	if !ok {
		return ""
	}
	for _, option := range options {
		trimmedOption := strings.TrimSpace(option)
		if trimmedOption == "" {
			continue
		}
		optionPolarity, optionOK := aiSearchJudgementPolarity(trimmedOption)
		if optionOK && optionPolarity == polarity {
			return trimmedOption
		}
	}
	return ""
}

func aiSearchJudgementPolarity(value string) (bool, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "正确", "对", "是", "√", "true", "t", "yes", "y":
		return true, true
	case "错误", "错", "否", "×", "false", "f", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func extractAISearchOptionIndex(answer string) int {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return -1
	}

	runes := []rune(strings.ToUpper(trimmed))
	if len(runes) == 0 || runes[0] < 'A' || runes[0] > 'Z' {
		if len(runes) >= 3 && (runes[0] == '(' || runes[0] == '（' || runes[0] == '[' || runes[0] == '【') && runes[1] >= 'A' && runes[1] <= 'Z' {
			closing := runes[2]
			if closing == ')' || closing == '）' || closing == ']' || closing == '】' {
				return int(runes[1] - 'A')
			}
		}
		return -1
	}

	if len(runes) == 1 {
		return int(runes[0] - 'A')
	}

	if unicode.IsSpace(runes[1]) {
		return int(runes[0] - 'A')
	}

	switch runes[1] {
	case '.', '、', ':', '：', ')', '）', ']', '】':
		return int(runes[0] - 'A')
	default:
		return -1
	}
}
