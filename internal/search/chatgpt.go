package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-resty/resty/v2"
	"github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ChatGptClient AI 搜题客户端
type ChatGptClient struct {
	Context context.Context
	Enable  bool
	APIKey  string
	BaseURL string
	Model   string
	// BackupModel is used only after the primary model exhausts retries for a
	// safety-policy rejection. Other upstream errors keep the existing retry
	// and failure behavior.
	BackupModel           string
	Prompt                string
	Temperature           float64
	TopP                  float64
	SearchTool            AISearchTool
	TikuSearchTool        AITikuSearchTool
	RequestID             string
	MaxAISearchModelCalls int
	MaxAISearches         int
	MaxAIWebReads         int
	// MaxAISearchRetries is the total number of attempts allowed for each
	// model round, including the initial request. Zero uses the default.
	MaxAISearchRetries int
	ContextCompression AIContextCompressionConfig
	// OCRProvider receives server-downloaded image bytes as an auxiliary signal;
	// the original visual image remains in the model request.
	OCRProvider      AIImageOCRProvider
	OCRCacheProvider AIImageOCRCacheProvider
	OCRAPI           string
}

type AISearchUsage struct {
	// Route identifies the effective execution path for this request.
	// normal is the one-shot model path; deep_native and deep_mcp are the
	// two deep-thinking tool orchestration paths.
	Route             string
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	TotalTokens       int64
	ModelCalls        []AIModelCallUsage       `json:"modelCalls,omitempty"`
	SearchCalls       []AISearchCallUsage      `json:"searchCalls,omitempty"`
	TikuSearchCalls   []AITikuSearchCallUsage  `json:"tikuSearchCalls,omitempty"`
	WebReadCalls      []AIWebReadCallUsage     `json:"webReadCalls,omitempty"`
	OCRCalls          []AIOCRCallUsage         `json:"ocrCalls,omitempty"`
	Attachments       []AIAttachmentDiagnostic `json:"attachments,omitempty"`
	Trace             []AISearchTrace          `json:"trace,omitempty"`
}

// AIAttachmentDiagnostic records bounded, URL-free attachment preparation
// details. SourceIndex follows the original question order; SentIndex is the
// one-based order among successful attachments sent to the model (zero means omitted).
type AIAttachmentDiagnostic struct {
	SourceIndex         int    `json:"sourceIndex"`
	SentIndex           int    `json:"sentIndex"`
	DownloadStatus      string `json:"downloadStatus"`
	FailureCode         string `json:"failureCode,omitempty"`
	HTTPStatus          int    `json:"httpStatus,omitempty"`
	OriginalContentType string `json:"originalContentType,omitempty"`
	DownloadedBytes     int    `json:"downloadedBytes,omitempty"`
	PreparedContentType string `json:"preparedContentType,omitempty"`
	PreparedBytes       int    `json:"preparedBytes,omitempty"`
	Preprocessing       string `json:"preprocessing,omitempty"`
}

// AIImageOCRProvider uploads bytes downloaded by the server. Implementations
// must not pass sourceURL to the OCR service as a remote URL.
type AIImageOCRProvider func(ctx context.Context, sourceURL, imageName, contentType string, image []byte) (string, error)

// AIImageOCRCacheProvider returns whether the OCR result was reused from the
// image cache. Cached OCR remains a billable OCR call.
type AIImageOCRCacheProvider func(ctx context.Context, sourceURL, imageName, contentType string, image []byte) (text string, cached bool, err error)

type AIOCRCallUsage struct {
	API         string `json:"api"`
	ImageURL    string `json:"imageUrl"`
	SourceIndex int    `json:"sourceIndex,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	ImageBytes  int    `json:"imageBytes,omitempty"`
	Status      string `json:"status"`
	DurationMs  int64  `json:"durationMs"`
	TextChars   int    `json:"textChars,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
	Error       string `json:"error,omitempty"`
}

const (
	AISearchRouteNormal     = "normal"
	AISearchRouteDeepNative = "deep_native"
	AISearchRouteDeepMCP    = "deep_mcp"
)

// AIModelCallUsage records one token-bearing response from the upstream model.
// Index is assigned in response order within the complete search request,
// including endpoint fallbacks and retry attempts.
type AIModelCallUsage struct {
	Index             int    `json:"index"`
	Model             string `json:"model"`
	Round             int    `json:"round"`
	Attempt           int    `json:"attempt"`
	InputTokens       int64  `json:"inputTokens"`
	CachedInputTokens int64  `json:"cachedInputTokens"`
	CacheWriteTokens  int64  `json:"cacheWriteTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	TotalTokens       int64  `json:"totalTokens"`
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

// AIWebReadCallUsage records one completed/failed web_read tool invocation.
// The URL is retained for private audit and billing snapshots; credentials are
// never part of the URL because the UAPI key is sent only as a header.
type AIWebReadCallUsage struct {
	API           string `json:"api"`
	URL           string `json:"url"`
	TaskID        string `json:"taskId,omitempty"`
	Status        string `json:"status"`
	DurationMs    int64  `json:"durationMs"`
	MarkdownBytes int    `json:"markdownBytes,omitempty"`
	Error         string `json:"error,omitempty"`
}

type AITikuSearchCallUsage struct {
	Query          string `json:"query"`
	Status         string `json:"status"`
	DurationMs     int64  `json:"durationMs"`
	MatchKind      string `json:"matchKind,omitempty"`
	CandidateCount int    `json:"candidateCount"`
	Error          string `json:"error,omitempty"`
}

type AISearchDebugResult struct {
	Endpoint    string
	RequestBody string
	RawResponse string
	Usage       AISearchUsage
	Trace       []AISearchTrace `json:"trace,omitempty"`
}

// AISearchTrace records the observable request/tool path. Full model output is
// written to the dedicated thinking log and omitted from the structured audit
// payload to keep database records bounded. RawResponse is retained for the
// debug response, but model SSE frames are deliberately not written to logs.
type AISearchTrace struct {
	Round                int     `json:"round"`
	Attempt              int     `json:"attempt,omitempty"`
	AttemptBudget        int     `json:"attemptBudget,omitempty"`
	Stage                string  `json:"stage"`
	Model                string  `json:"model"`
	Endpoint             string  `json:"endpoint"`
	Query                string  `json:"query,omitempty"`
	SearchAPI            string  `json:"searchApi,omitempty"`
	SourceDetails        string  `json:"sourceDetails,omitempty"`
	ResultSummary        string  `json:"resultSummary,omitempty"`
	Status               string  `json:"status,omitempty"`
	DurationMs           int64   `json:"durationMs,omitempty"`
	ModelCallCount       int     `json:"modelCallCount,omitempty"`
	ModelCallIndex       int     `json:"modelCallIndex,omitempty"`
	ModelCallBudget      int     `json:"modelCallBudget,omitempty"`
	RemainingModelCalls  int     `json:"remainingModelCalls"`
	SearchCount          int     `json:"searchCount,omitempty"`
	SearchBudget         int     `json:"searchBudget,omitempty"`
	RemainingSearches    int     `json:"remainingSearches"`
	RemainingWebReads    int     `json:"remainingWebReads"`
	ResultCount          int     `json:"resultCount,omitempty"`
	CacheHit             bool    `json:"cacheHit,omitempty"`
	ReasoningChars       int     `json:"reasoningChars,omitempty"`
	InputTokens          int64   `json:"inputTokens,omitempty"`
	CachedInputTokens    int64   `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens     int64   `json:"cacheWriteTokens,omitempty"`
	OutputTokens         int64   `json:"outputTokens,omitempty"`
	TotalTokens          int64   `json:"totalTokens,omitempty"`
	StageBillingMode     string  `json:"stageBillingMode,omitempty"`
	StageMultiplier      float64 `json:"stageMultiplier,omitempty"`
	FinalMultiplier      float64 `json:"finalMultiplier,omitempty"`
	MultiplierApplicable bool    `json:"multiplierApplicable,omitempty"`
	ContentPreview       string  `json:"contentPreview,omitempty"`
	Error                string  `json:"error,omitempty"`
	RequestBody          string  `json:"-"`
	FullResponse         string  `json:"-"`
	RawResponse          string  `json:"-"`
}

func (in *ChatGptClient) prepareAISearchTrace(trace AISearchTrace) AISearchTrace {
	if strings.TrimSpace(trace.Model) == "" {
		trace.Model = strings.TrimSpace(in.Model)
	}
	trace.Endpoint = logger.RedactSecrets(trace.Endpoint, in.APIKey)
	trace.SearchAPI = logger.RedactSecrets(trace.SearchAPI, in.APIKey)
	trace.Query = truncateAIToolText(logger.RedactSecrets(trace.Query, in.APIKey), 120)
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

func buildAIModelCallUsage(usage AISearchUsage, model string, round int, attempt int) (AIModelCallUsage, bool) {
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.InputTokens + usage.OutputTokens
	}
	if totalTokens <= 0 {
		return AIModelCallUsage{}, false
	}
	return AIModelCallUsage{
		Model:             strings.TrimSpace(model),
		Round:             round,
		Attempt:           attempt,
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		CacheWriteTokens:  usage.CacheWriteTokens,
		OutputTokens:      usage.OutputTokens,
		TotalTokens:       totalTokens,
	}, true
}

func appendAIModelCallUsage(usage *AISearchUsage, call AIModelCallUsage) {
	if usage == nil {
		return
	}
	call.Index = len(usage.ModelCalls) + 1
	usage.ModelCalls = append(usage.ModelCalls, call)
}

func applyAIModelCallUsageToTrace(trace *AISearchTrace, call AIModelCallUsage) {
	if trace == nil {
		return
	}
	trace.ModelCallIndex = call.Index
	trace.InputTokens = call.InputTokens
	trace.CachedInputTokens = call.CachedInputTokens
	trace.CacheWriteTokens = call.CacheWriteTokens
	trace.OutputTokens = call.OutputTokens
	trace.TotalTokens = call.TotalTokens
}

func offsetAIModelCallIndexes(traces []AISearchTrace, offset int) {
	if offset <= 0 {
		return
	}
	for i := range traces {
		if traces[i].ModelCallIndex > 0 {
			traces[i].ModelCallIndex += offset
		}
	}
}

func (in *ChatGptClient) logAISearchTrace(trace AISearchTrace) {
	structuredTrace := in.prepareAISearchTrace(trace)
	requestID := strings.TrimSpace(in.RequestID)
	tokenDetails := ""
	if structuredTrace.ModelCallIndex > 0 {
		tokenDetails = fmt.Sprintf(" modelCallIndex=%d tokens=%d inputTokens=%d cachedInputTokens=%d cacheWriteTokens=%d outputTokens=%d", structuredTrace.ModelCallIndex, structuredTrace.TotalTokens, structuredTrace.InputTokens, structuredTrace.CachedInputTokens, structuredTrace.CacheWriteTokens, structuredTrace.OutputTokens)
	}
	logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜索轨迹 requestId=%s round=%d attempt=%d/%d stage=%s model=%s endpoint=%s searchAPI=%s query=%s status=%s durationMs=%d modelCalls=%d/%d%s remainingModelCalls=%d searches=%d/%d remainingSearches=%d remainingWebReads=%d results=%d cacheHit=%t sources=%s resultSummary=%s reasoningChars=%d contentPreview=%s error=%s", requestID, structuredTrace.Round, structuredTrace.Attempt, structuredTrace.AttemptBudget, structuredTrace.Stage, strings.TrimSpace(structuredTrace.Model), structuredTrace.Endpoint, structuredTrace.SearchAPI, structuredTrace.Query, structuredTrace.Status, structuredTrace.DurationMs, structuredTrace.ModelCallCount, structuredTrace.ModelCallBudget, tokenDetails, structuredTrace.RemainingModelCalls, structuredTrace.SearchCount, structuredTrace.SearchBudget, structuredTrace.RemainingSearches, structuredTrace.RemainingWebReads, structuredTrace.ResultCount, structuredTrace.CacheHit, structuredTrace.SourceDetails, structuredTrace.ResultSummary, structuredTrace.ReasoningChars, structuredTrace.ContentPreview, structuredTrace.Error), in.APIKey)
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
	// RawResponse for model calls is the wire-level SSE body. It is useful to
	// callers of SearchAnswerDebug, but logging it would emit every `data:`
	// chunk and make the thinking log noisy. Search/web API responses are normal
	// complete payloads, so retain those in the detailed trace.
	if trace.RawResponse != "" && strings.TrimSpace(trace.SearchAPI) != "" {
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜索API完整响应 requestId=%s round=%d stage=%s\n%s", requestID, trace.Round, trace.Stage, trace.RawResponse), in.APIKey)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type openAIResponsesRequest struct {
	Model             string                    `json:"model"`
	Stream            bool                      `json:"stream"`
	Store             bool                      `json:"store"`
	Input             []any                     `json:"input"`
	Include           []string                  `json:"include,omitempty"`
	Tools             []openAIResponsesTool     `json:"tools,omitempty"`
	ToolChoice        any                       `json:"tool_choice,omitempty"`
	MaxOutputTokens   int                       `json:"max_output_tokens,omitempty"`
	ContextManagement []openAIContextManagement `json:"context_management,omitempty"`
}

type openAIContextManagement struct {
	Type             string `json:"type"`
	CompactThreshold int64  `json:"compact_threshold"`
}

type openAIResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict"`
}

type openAIResponsesMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openAIResponsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type openAIContentPart struct {
	Type     string              `json:"type"`
	Text     string              `json:"text,omitempty"`
	ImageURL *openAIImageURLPart `json:"image_url,omitempty"`
}

type openAIImageURLPart struct {
	URL string `json:"url"`
}

type openAIResponsesEvent struct {
	Type         string          `json:"type"`
	Delta        string          `json:"delta,omitempty"`
	Text         string          `json:"text,omitempty"`
	Arguments    string          `json:"arguments,omitempty"`
	OutputIndex  int             `json:"output_index,omitempty"`
	ContentIndex int             `json:"content_index,omitempty"`
	ItemID       string          `json:"item_id,omitempty"`
	Item         json.RawMessage `json:"item,omitempty"`
	Error        json.RawMessage `json:"error,omitempty"`
	Response     struct {
		ID                string                `json:"id"`
		Status            string                `json:"status"`
		Output            []json.RawMessage     `json:"output"`
		Usage             *openAIResponsesUsage `json:"usage,omitempty"`
		Error             json.RawMessage       `json:"error,omitempty"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details,omitempty"`
	} `json:"response,omitempty"`
}

type openAIResponsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	InputDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

type openAIResponsesOutputItem struct {
	Type             string            `json:"type"`
	ID               string            `json:"id,omitempty"`
	CallID           string            `json:"call_id,omitempty"`
	Name             string            `json:"name,omitempty"`
	Arguments        string            `json:"arguments,omitempty"`
	Status           string            `json:"status,omitempty"`
	Role             string            `json:"role,omitempty"`
	Content          []json.RawMessage `json:"content,omitempty"`
	EncryptedContent string            `json:"encrypted_content,omitempty"`
	Summary          []json.RawMessage `json:"summary,omitempty"`
}

type aiSearchAnswerPayload struct {
	Answers []string `json:"answers"`
}

// aiResponsesUpstreamError preserves structured error metadata emitted in a
// 2xx SSE response. A typed error is needed because the HTTP status alone is
// not sufficient to decide whether a stream failure is safe to retry.
type aiResponsesUpstreamError struct {
	Code    string
	Type    string
	Message string
	Raw     string
}

func (e *aiResponsesUpstreamError) Error() string {
	parts := make([]string, 0, 3)
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.Type != "" {
		parts = append(parts, "type="+e.Type)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if len(parts) == 0 {
		return "AI Responses upstream error event"
	}
	return "AI Responses upstream error event: " + strings.Join(parts, ", ")
}

func (e *aiResponsesUpstreamError) Unwrap() error { return errors.ErrTargetServerError }

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
//   - aiSearchRetryMaxAttempts / aiSearchRetryBaseDelay 只处理明确未执行的上游拒绝状态。
const (
	aiSearchMaxConcurrentRequests    = 8
	aiSearchMaxConcurrentAttachments = 4
	aiSearchRetryMaxAttempts         = 3
	aiSearchRetryBaseDelay           = 500 * time.Millisecond
	aiSearchRetryMaxDelay            = 5 * time.Second
)

func (in *ChatGptClient) aiSearchRetryAttempts() int {
	if in.MaxAISearchRetries <= 0 {
		return aiSearchRetryMaxAttempts
	}
	return in.MaxAISearchRetries
}

func (in *ChatGptClient) requestContext() context.Context {
	if in.Context != nil {
		return in.Context
	}
	return context.Background()
}

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

【准确性与完整交付】
- 准确率是最高优先级：区分已核实事实与推断，不得编造事实、引用或执行结果；条件缺失时说明必要假设及适用范围，不能把猜测写成已核实的结论。不得用推脱式回答代替分析，但也不得为显得确定而虚构依据。
- 仅在本轮实际提供联网工具时，正式联网答题必须先完成一次搜索，核对问题相关的最新事实，不能只凭模型知识库作答；必要时使用网页浏览核对原始正文和上下文，避免仅凭搜索摘要下结论。管理员工具专项测试遵循所选工具和指定顺序；未提供工具或本轮只允许最终作答时，基于已有资料完成答案，不声称已联网核验。
- 复杂项目、编程及综合作业应覆盖全部核心要求，在答案文本中组织必要的核心代码、配置及验证步骤，先保证关键流程完整，再补充细节；受篇幅限制时明确交付范围及尚待补充的部分，不能只写计划或只回答其中一问。
- 本接口交付文本答案；没有实际执行能力时，不得声称已经创建文件、运行构建或通过测试，将需要实际执行的操作列为验证步骤。

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
	ctx, cancel := context.WithCancel(in.requestContext())
	defer cancel()
	if req.DeepThinking && in.SearchTool != nil {
		answer, debug, toolErr := in.searchAnswerWithTools(ctx, req)
		if toolErr == nil {
			debug.Usage.Route = AISearchRouteDeepNative
			return answer, debug, nil
		}
		mcpAnswer, mcpDebug, handled, mcpErr := in.fallbackToMCPAfterNativeFailure(ctx, req, debug, toolErr)
		if handled {
			return mcpAnswer, mcpDebug, mcpErr
		}
		return answer, mcpDebug, toolErr
	}
	answer = make([][]string, 0)
	debug.Usage.Route = AISearchRouteNormal
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
	userPromptParts, ocrCalls, attachmentDiagnostics := in.buildAISearchPromptPartsWithOCRDetailed(ctx, req, in.getAttachmentHTTPClient().GetClient())
	endpoints := buildAIResponsesEndpoints(baseURL)
	maxModelCalls, _, _ := in.aiSearchBudgets()
	modelCalls := 0
	content, usage, _, endpoint, requestBody, rawResponse, callErr := in.requestStreamAnswerDetailed(ctx, endpoints, systemPrompt, userPromptParts, &modelCalls, maxModelCalls)
	usage.OCRCalls = append(usage.OCRCalls, ocrCalls...)
	usage.Attachments = append(usage.Attachments, attachmentDiagnostics...)
	debug = AISearchDebugResult{
		Endpoint:    endpoint,
		RequestBody: requestBody,
		RawResponse: rawResponse,
		Usage:       usage,
		Trace:       append([]AISearchTrace(nil), usage.Trace...),
	}
	debug.Usage.Route = AISearchRouteNormal
	if callErr != nil {
		return nil, debug, callErr
	}
	parsedAnswers := parseAISearchAnswers(content, req.Options, req.Type)
	if len(parsedAnswers) == 0 {
		return nil, debug, errors.ErrTargetNoAnswer
	}
	return [][]string{parsedAnswers}, debug, nil
}

func (in *ChatGptClient) SearchAnswerWithUsage(req model.SearchRequest) (answer [][]string, usage AISearchUsage, err error) {
	ctx, cancel := context.WithCancel(in.requestContext())
	defer cancel()
	if req.DeepThinking && in.SearchTool != nil {
		answer, debug, toolErr := in.searchAnswerWithTools(ctx, req)
		if toolErr == nil {
			debug.Usage.Route = AISearchRouteDeepNative
			return answer, debug.Usage, nil
		}
		mcpAnswer, mcpDebug, handled, mcpErr := in.fallbackToMCPAfterNativeFailure(ctx, req, debug, toolErr)
		if handled {
			return mcpAnswer, mcpDebug.Usage, mcpErr
		}
		return answer, mcpDebug.Usage, toolErr
	}
	if req.DeepThinking {
		logger.SysLog(fmt.Sprintf("AI 搜索降级 requestId=%s 原因=未配置联网搜索工具 行为=保持旧版一次性模型回复", strings.TrimSpace(req.RequestID)))
	}
	answer = make([][]string, 0)
	usage.Route = AISearchRouteNormal
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
	userPromptParts, ocrCalls, attachmentDiagnostics := in.buildAISearchPromptPartsWithOCRDetailed(ctx, req, in.getAttachmentHTTPClient().GetClient())
	endpoints := buildAIResponsesEndpoints(baseURL)
	maxModelCalls, _, _ := in.aiSearchBudgets()
	modelCalls := 0
	content, streamUsage, _, _, _, _, callErr := in.requestStreamAnswerDetailed(ctx, endpoints, systemPrompt, userPromptParts, &modelCalls, maxModelCalls)
	streamUsage.OCRCalls = append(streamUsage.OCRCalls, ocrCalls...)
	streamUsage.Attachments = append(streamUsage.Attachments, attachmentDiagnostics...)
	streamUsage.Route = AISearchRouteNormal
	if callErr != nil {
		return nil, streamUsage, callErr
	}
	parsedAnswers := parseAISearchAnswers(content, req.Options, req.Type)
	if len(parsedAnswers) == 0 {
		return nil, streamUsage, errors.ErrTargetNoAnswer
	}
	return [][]string{parsedAnswers}, streamUsage, nil
}

func buildAISearchPrompt(req model.SearchRequest) string {
	var builder strings.Builder
	builder.WriteString("请根据系统规则返回答案 JSON。")
	builder.WriteString("\n题型：")
	questionType := effectiveAISearchQuestionType(req)
	builder.WriteString(resolveAISearchQuestionTypeLabel(questionType))
	builder.WriteString("\n题目：")
	builder.WriteString(strings.TrimSpace(req.Question))
	switch questionType {
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
	if evidence := req.QuestionEvidence; evidence != nil {
		builder.WriteString("\n客户端图片证据（按页面出现顺序；alt仅为辅助文字，需核验图片）：")
		appendImages := func(label string, images []model.QuestionImageEvidence) {
			for index, image := range images {
				if index >= 100 {
					break
				}
				fmt.Fprintf(&builder, "\n%s图%d：alt=%s url=%s", label, image.Index+1, truncateAITikuText(image.Alt, 2048), truncateAITikuText(image.URL, 2048))
			}
		}
		appendImages("题干", evidence.StemImages)
		appendImages("选项", evidence.OptionImages)
	}
	builder.WriteString("\n作答要求：如果题目包含多个小问，请按原编号完整回答每一问，不要只回答其中一问或随机选择一个小问。")
	builder.WriteString("\n作答要求：不要返回“不确定”“不知道”或“无法确定”；请综合题干、选项、图片、课程与学科常识给出最可能的完整答案。")
	return builder.String()
}

// effectiveAISearchQuestionType 返回用于提示词的题型。
//
// 客户端偶尔把单选题标成 3（判断题），却带着 A-D 选项：此时按判断题提问，
// 模型会回答「正确/错误」，而选项里根本没有这两个值 → 明明答了却得到空答案
// （线上 178 条空答案回放里占 26 条）。只在「type=3 且选项完全没有对/错语义」时
// 按 0（单选）提问；选项是 正确/错误 的判断题不受影响。
func effectiveAISearchQuestionType(req model.SearchRequest) int {
	if req.Type == 3 && len(req.Options) > 0 && !hasAISearchJudgementOptions(req.Options) {
		return 0
	}
	return req.Type
}

// hasAISearchJudgementOptions 判断选项里是否至少有一个表达「对/错」语义。
func hasAISearchJudgementOptions(options []string) bool {
	for _, option := range options {
		// FormatOptions 会去掉 "A. " 前缀并把 对/错 规范成 正确/错误。
		normalized := util.FormatOptions([]string{option}, 3)
		if len(normalized) == 0 {
			continue
		}
		if _, ok := util.JudgementPolarity(normalized[0]); ok {
			return true
		}
	}
	return false
}

func resolveAISearchQuestionTypeLabel(questionType int) string {
	switch questionType {
	case -1:
		return "未知题型（仅根据可见结构判断，不预设为单选题）"
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

func (in *ChatGptClient) buildAISearchPromptPartsWithOCR(ctx context.Context, req model.SearchRequest, httpClient *http.Client) ([]openAIContentPart, []AIOCRCallUsage) {
	parts, calls, _ := in.buildAISearchPromptPartsWithOCRDetailed(ctx, req, httpClient)
	return parts, calls
}

func (in *ChatGptClient) buildAISearchPromptPartsWithOCRDetailed(ctx context.Context, req model.SearchRequest, httpClient *http.Client) ([]openAIContentPart, []AIOCRCallUsage, []AIAttachmentDiagnostic) {
	parts := []openAIContentPart{{Type: "text", Text: buildAISearchPrompt(req)}}
	urls := extractAISearchURLs(req)
	attachments, diagnostics := downloadAISearchAttachmentResultsDetailed(httpClient, urls)
	sentIndex := 1
	for _, attachment := range attachments {
		parts = append(parts, attachment.part)
		for index := range diagnostics {
			if diagnostics[index].SourceIndex == attachment.sourceIndex {
				diagnostics[index].SentIndex = sentIndex
				break
			}
		}
		sentIndex++
	}
	if references := buildAISearchAttachmentReferences(urls, attachments); references != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: references})
	}
	ocrText, calls := in.runAISearchImageOCR(ctx, attachments)
	if strings.TrimSpace(ocrText) != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: ocrText})
	}
	return parts, calls, diagnostics
}

type downloadedAISearchAttachment struct {
	sourceIndex int
	sourceURL   string
	imageName   string
	contentType string
	body        []byte
	part        openAIContentPart
}

// buildAISearchAttachmentReferences keeps original image numbers even when
// one or more downloads fail. The model still receives the text question and
// every successful image, so a partial download never turns into a hard
// no-answer path or silently remaps option C to option B.
func buildAISearchAttachmentReferences(urls []string, attachments []downloadedAISearchAttachment) string {
	if len(urls) == 0 {
		return ""
	}
	byURL := make(map[string]downloadedAISearchAttachment, len(attachments))
	for index, attachment := range attachments {
		if strings.TrimSpace(attachment.sourceURL) == "" {
			continue
		}
		if attachment.sourceIndex <= 0 {
			attachment.sourceIndex = index + 1
		}
		byURL[attachment.sourceURL] = attachment
	}
	var references strings.Builder
	references.WriteString("图片对应关系（视觉图片按成功下载顺序提供，图号沿用原题顺序；缺失图片不应阻止作答）：")
	missing := 0
	for index, address := range urls {
		if _, ok := byURL[address]; ok {
			fmt.Fprintf(&references, "\n图%s为：%s", formatAISearchImageIndex(index+1), address)
			continue
		}
		missing++
		fmt.Fprintf(&references, "\n图%s下载失败：%s（请结合题干和其他可见图片继续作答）", formatAISearchImageIndex(index+1), address)
	}
	if missing > 0 {
		fmt.Fprintf(&references, "\n共有%d张图片未成功下载；不要仅因缺图返回不确定，除非现有文字和图片确实不足以判断。", missing)
	}
	return references.String()
}

func (in *ChatGptClient) runAISearchImageOCR(ctx context.Context, attachments []downloadedAISearchAttachment) (string, []AIOCRCallUsage) {
	if in == nil || (in.OCRProvider == nil && in.OCRCacheProvider == nil) || len(attachments) == 0 {
		return "", nil
	}
	images := make([]downloadedAISearchAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.contentType)), "image/") {
			images = append(images, attachment)
		}
	}
	if len(images) == 0 {
		return "", nil
	}
	type result struct {
		index         int
		originalIndex int
		text          string
		call          AIOCRCallUsage
	}
	results := make(chan result, len(images))
	semaphore := make(chan struct{}, aiSearchMaxConcurrentAttachments)
	var wg sync.WaitGroup
	for index, attachment := range images {
		wg.Add(1)
		go func(index int, attachment downloadedAISearchAttachment) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			text, call := in.recognizeAISearchImage(ctx, attachment)
			originalIndex := attachment.sourceIndex
			if originalIndex <= 0 {
				originalIndex = index + 1
			}
			results <- result{index: index, originalIndex: originalIndex, text: text, call: call}
		}(index, attachment)
	}
	wg.Wait()
	close(results)
	ordered := make([]result, len(images))
	for item := range results {
		ordered[item.index] = item
	}
	var builder strings.Builder
	for _, item := range ordered {
		if item.call.Status == "success" && strings.TrimSpace(item.text) != "" {
			if builder.Len() == 0 {
				builder.WriteString("OCR辅助识别结果（由服务器下载图片后识别，仅作为视觉图片的辅助文本，原图仍已发送）：")
			}
			fmt.Fprintf(&builder, "\n图%s OCR：%s", formatAISearchImageIndex(item.originalIndex), item.text)
		}
	}
	calls := make([]AIOCRCallUsage, 0, len(ordered))
	for _, item := range ordered {
		if item.call.Status != "" {
			calls = append(calls, item.call)
		}
	}
	return builder.String(), calls
}

// recognizeAISearchImage is shared by production prompts and direct diagnostics.
// Keep the recognized text separate from the model-facing image annotations.
func (in *ChatGptClient) recognizeAISearchImage(ctx context.Context, attachment downloadedAISearchAttachment) (string, AIOCRCallUsage) {
	started := time.Now()
	var text string
	var cached bool
	var err error
	if in.OCRCacheProvider != nil {
		text, cached, err = in.OCRCacheProvider(ctx, attachment.sourceURL, attachment.imageName, attachment.contentType, attachment.body)
	} else {
		text, err = in.OCRProvider(ctx, attachment.sourceURL, attachment.imageName, attachment.contentType, attachment.body)
	}
	call := AIOCRCallUsage{API: strings.TrimSpace(in.OCRAPI), ImageURL: attachment.sourceURL, SourceIndex: attachment.sourceIndex, ContentType: attachment.contentType, ImageBytes: len(attachment.body), DurationMs: time.Since(started).Milliseconds()}
	if call.API == "" {
		call.API = "https://uapis.cn/api/v1/image/ocr"
	}
	if err != nil {
		call.Status = "error"
		call.Error = truncateAIToolText(logger.RedactSecrets(err.Error(), in.APIKey), 500)
		return "", call
	}
	text = strings.TrimSpace(text)
	call.Status, call.Cached, call.TextChars = "success", cached, len([]rune(text))
	return truncateAIToolText(text, 12000), call
}

// TestImageOCR downloads one image using the same bounded attachment path as
// normal AI search and runs the configured OCR provider against local bytes.
// It is used by the administrator's direct OCR diagnostic and keeps the raw
// image URL out of the upstream OCR request.
func (in *ChatGptClient) TestImageOCR(ctx context.Context, address string) (string, AISearchDebugResult, error) {
	debug := AISearchDebugResult{}
	address = strings.TrimSpace(address)
	parsed, err := url.Parse(address)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return "", debug, fmt.Errorf("请填写不含用户信息和片段的完整 HTTP/HTTPS 图片地址")
	}
	if in == nil || (in.OCRProvider == nil && in.OCRCacheProvider == nil) {
		return "", debug, stdErrors.New("未配置图片 OCR 工具，请检查 UApiPro 令牌")
	}
	if len([]rune(address)) > aiToolMaxWebReadURLRunes {
		return "", debug, stdErrors.New("图片地址不能超过 2048 个字符")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bounded := *in.getAttachmentHTTPClient().GetClient()
	transport := bounded.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	bounded.Transport = aiToolContextTransport{ctx: ctx, base: transport}
	attachment, diagnostic, ok := downloadAISearchAttachmentDetailed(&bounded, address, 1)
	debug.Usage.Attachments = append(debug.Usage.Attachments, diagnostic)
	if !ok {
		return "", debug, fmt.Errorf("图片下载失败")
	}
	if !strings.HasPrefix(attachment.contentType, "image/") {
		return "", debug, stdErrors.New("目标地址未返回图片")
	}
	text, call := in.recognizeAISearchImage(ctx, attachment)
	debug.Usage.OCRCalls = append(debug.Usage.OCRCalls, call)
	if call.Status != "success" {
		return text, debug, stdErrors.New(call.Error)
	}
	if strings.TrimSpace(text) == "" {
		return text, debug, stdErrors.New("OCR 未返回识别文本")
	}
	return text, debug, nil
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

// downloadAISearchAttachmentResults keeps the successful source URL together
// with the downloaded bytes. This prevents a failed middle image from making
// the remaining image references point at the wrong visual attachment.
func downloadAISearchAttachmentResults(httpClient *http.Client, attachmentURLs []string) []downloadedAISearchAttachment {
	attachments, _ := downloadAISearchAttachmentResultsDetailed(httpClient, attachmentURLs)
	return attachments
}

func downloadAISearchAttachmentResultsDetailed(httpClient *http.Client, attachmentURLs []string) ([]downloadedAISearchAttachment, []AIAttachmentDiagnostic) {
	if httpClient == nil || len(attachmentURLs) == 0 {
		return nil, nil
	}
	type attachmentResult struct {
		index      int
		item       downloadedAISearchAttachment
		diagnostic AIAttachmentDiagnostic
		ok         bool
	}
	semaphore := make(chan struct{}, aiSearchMaxConcurrentAttachments)
	results := make(chan attachmentResult, len(attachmentURLs))
	var wg sync.WaitGroup
	for index, address := range attachmentURLs {
		wg.Add(1)
		go func(index int, address string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			item, diagnostic, ok := downloadAISearchAttachmentDetailed(httpClient, address, index+1)
			results <- attachmentResult{index: index, item: item, diagnostic: diagnostic, ok: ok}
		}(index, address)
	}
	wg.Wait()
	close(results)
	ordered := make([]attachmentResult, len(attachmentURLs))
	for item := range results {
		ordered[item.index] = item
	}
	attachments := make([]downloadedAISearchAttachment, 0, len(attachmentURLs))
	diagnostics := make([]AIAttachmentDiagnostic, len(attachmentURLs))
	for index, item := range ordered {
		diagnostics[index] = item.diagnostic
		if item.ok {
			item.item.sourceIndex = index + 1
			attachments = append(attachments, item.item)
		}
	}
	return attachments, diagnostics
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
	if evidence := req.QuestionEvidence; evidence != nil {
		for _, images := range [][]model.QuestionImageEvidence{evidence.StemImages, evidence.OptionImages} {
			for index, image := range images {
				if index >= 100 {
					break
				}
				parsed, err := url.Parse(strings.TrimSpace(image.URL))
				if err == nil && parsed.Hostname() != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					appendMatches(parsed.String())
				}
			}
		}
	}
	appendMatches(req.Question)
	for _, option := range req.Options {
		appendMatches(option)
	}
	return urls
}

func downloadAISearchAttachmentPart(httpClient *http.Client, attachmentURL string) (openAIContentPart, bool) {
	attachment, ok := downloadAISearchAttachment(httpClient, attachmentURL)
	if !ok {
		return openAIContentPart{}, false
	}
	return attachment.part, true
}

func downloadAISearchAttachment(httpClient *http.Client, attachmentURL string) (downloadedAISearchAttachment, bool) {
	attachment, _, ok := downloadAISearchAttachmentDetailed(httpClient, attachmentURL, 0)
	return attachment, ok
}

func downloadAISearchAttachmentDetailed(httpClient *http.Client, attachmentURL string, sourceIndex int) (downloadedAISearchAttachment, AIAttachmentDiagnostic, bool) {
	diagnostic := AIAttachmentDiagnostic{SourceIndex: sourceIndex, DownloadStatus: "error"}
	if httpClient == nil {
		diagnostic.FailureCode = "no_client"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	parsed, err := url.Parse(strings.TrimSpace(attachmentURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		diagnostic.FailureCode = "invalid_url"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	httpReq, err := http.NewRequest(http.MethodGet, attachmentURL, nil)
	if err != nil {
		diagnostic.FailureCode = "request_error"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	httpReq.Header.Set("Referer", "https://mooc1.chaoxing.com/")
	httpReq.Header.Set("Origin", "https://mooc1.chaoxing.com")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := util.DoReadOnlyWithRetry(httpClient, httpReq)
	if err != nil {
		diagnostic.FailureCode = "network_error"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		diagnostic.HTTPStatus = resp.StatusCode
		diagnostic.FailureCode = "http_status"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	if resp.ContentLength > maxAISearchAttachmentBytes {
		diagnostic.FailureCode = "too_large"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAISearchAttachmentBytes+1))
	if err != nil || len(body) == 0 {
		diagnostic.FailureCode = "read_error"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
	if len(body) > maxAISearchAttachmentBytes {
		diagnostic.FailureCode = "too_large"
		return downloadedAISearchAttachment{}, diagnostic, false
	}

	contentType := resolveAISearchAttachmentContentType(resp.Header.Values("Content-Type"), body)
	diagnostic.OriginalContentType = contentType
	diagnostic.DownloadedBytes = len(body)
	switch {
	case strings.HasPrefix(contentType, "image/"):
		preparedBody, preparedContentType, preprocessing := prepareAISearchImageAttachmentDetailed(body, contentType)
		diagnostic.PreparedContentType = preparedContentType
		diagnostic.PreparedBytes = len(preparedBody)
		diagnostic.Preprocessing = preprocessing
		ocrBody, ocrContentType := body, contentType
		if preparedContentType == "image/png" {
			// The prepared PNG is the locally converted representation. Reusing it
			// for OCR keeps WebP/BMP/TIFF and transparent formula images compatible
			// with the multipart endpoint while preserving the original source URL.
			ocrBody, ocrContentType = preparedBody, preparedContentType
		}
		imageName := filepath.Base(parsed.Path)
		if imageName == "." || imageName == "" || filepath.Ext(imageName) == "" || (ocrContentType == "image/png" && strings.ToLower(filepath.Ext(imageName)) != ".png") {
			imageName = "image.png"
		}
		diagnostic.DownloadStatus = "success"
		return downloadedAISearchAttachment{sourceURL: attachmentURL, sourceIndex: sourceIndex, imageName: imageName, contentType: ocrContentType, body: ocrBody, part: openAIContentPart{
			Type: "image_url",
			ImageURL: &openAIImageURLPart{
				URL: "data:" + preparedContentType + ";base64," + base64.StdEncoding.EncodeToString(preparedBody),
			},
		}}, diagnostic, true
	case contentType == "text/html":
		diagnostic.DownloadStatus = "success"
		return downloadedAISearchAttachment{sourceURL: attachmentURL, sourceIndex: sourceIndex, imageName: filepath.Base(parsed.Path), contentType: contentType, body: body, part: openAIContentPart{
			Type: "text",
			Text: buildAISearchHTMLAttachment(parsed, body),
		}}, diagnostic, true
	default:
		diagnostic.FailureCode = "unsupported_content_type"
		return downloadedAISearchAttachment{}, diagnostic, false
	}
}

// prepareAISearchImageAttachment 规范化题目配图后再提交给多模态模型。
//
// 题库（例如超星）返回的公式截图通常是「透明背景 + 黑色字形」的小图。
// 这类图片一旦被上游按黑底解码，就会变成「黑字黑底」的全黑图，
// 模型只能看到一张空图，于是统一回答“不确定”——这正是公式题答不出来的根因。
// 因此这里统一把透明像素合成到白底，并适度放大过小的公式图。
func prepareAISearchImageAttachment(body []byte, contentType string) ([]byte, string) {
	preparedBody, preparedContentType, _ := prepareAISearchImageAttachmentDetailed(body, contentType)
	return preparedBody, preparedContentType
}

func prepareAISearchImageAttachmentDetailed(body []byte, contentType string) ([]byte, string, string) {
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return body, contentType, "decode_failed"
	}

	prepared := flattenAISearchImageOntoWhite(decoded)
	if prepared == nil {
		return body, contentType, "preparation_failed"
	}
	prepared = upscaleAISearchImage(prepared)

	var buf bytes.Buffer
	if err := png.Encode(&buf, prepared); err != nil {
		return body, contentType, "encode_failed"
	}
	if buf.Len() == 0 {
		return body, contentType, "encode_failed"
	}
	if buf.Len() > maxAISearchAttachmentBytes {
		return body, contentType, "size_fallback"
	}
	// 统一输出 PNG：无损、保留字形的锐利边缘，避免 JPEG 压缩糊掉下标和分数。
	return buf.Bytes(), "image/png", "png_normalized"
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

func buildAIResponsesEndpoints(baseURL string) []string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		return nil
	}

	endpoints := []string{normalized + "/responses"}
	if !strings.HasSuffix(normalized, "/v1") {
		endpoints = append(endpoints, normalized+"/v1/responses")
	}
	return endpoints
}

func buildAIResponsesMessage(role string, content any) openAIResponsesMessage {
	return openAIResponsesMessage{Role: role, Content: content}
}

func buildAIResponsesContentParts(parts []openAIContentPart) []openAIResponsesContentPart {
	converted := make([]openAIResponsesContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "image_url":
			if part.ImageURL != nil && strings.TrimSpace(part.ImageURL.URL) != "" {
				converted = append(converted, openAIResponsesContentPart{Type: "input_image", ImageURL: part.ImageURL.URL})
			}
		default:
			if part.Type == "text" && part.Text != "" {
				converted = append(converted, openAIResponsesContentPart{Type: "input_text", Text: part.Text})
			}
		}
	}
	return converted
}

func (in *ChatGptClient) requestStreamAnswerDetailed(parentCtx context.Context, endpoints []string, systemPrompt string, userPromptParts []openAIContentPart, modelCalls *int, maxModelCalls int) (string, AISearchUsage, int, string, string, string, error) {
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

	modelName := strings.TrimSpace(in.Model)
	backupUsed := false
	if *modelCalls >= maxModelCalls {
		return "", lastUsage, lastStatusCode, "", requestBody, lastRawResponse, lastErr
	}
	*modelCalls++
	// A retry is another chance for the same logical model round. It must not
	// consume the configured model-round budget, otherwise maxModelCalls=1
	// would silently disable the retry setting. A backup model is entered only
	// after the primary exhausts its complete retry budget for invalid_prompt.
	for {
		attempted := 0
		for attempt := 0; attempt < in.aiSearchRetryAttempts(); attempt++ {
			if attempt > 0 {
				if !sleepAISearchRetryBackoff(parentCtx, attempt, lastErr) {
					break
				}
			}

			attempted++
			payload, err := json.Marshal(openAIResponsesRequest{
				Model:  modelName,
				Stream: true,
				Store:  false,
				Input: []any{
					buildAIResponsesMessage("system", systemPrompt+"\n本轮不提供任何联网/读取工具，请直接输出最终答案 JSON。"),
					buildAIResponsesMessage("user", buildAIResponsesContentParts(userPromptParts)),
				},
			})
			if err != nil {
				return "", AISearchUsage{}, 0, "", "", "", errors.ErrParserJSON
			}
			var attemptContent string
			var attemptUsage AISearchUsage
			var attemptEndpoint string
			var attemptStatus int
			var attemptRawResponse string
			var attemptErr error
			var attemptRetryable bool
			for endpointIndex, endpoint := range endpoints {
				requestBody = string(payload)
				remainingAfterCall := maxModelCalls - *modelCalls
				recordTrace(AISearchTrace{Round: *modelCalls, Attempt: attempt + 1, AttemptBudget: in.aiSearchRetryAttempts(), Stage: "普通模型调用开始", Endpoint: endpoint, Model: modelName, ModelCallCount: *modelCalls, ModelCallBudget: maxModelCalls, RemainingModelCalls: remainingAfterCall})
				attemptStartedAt := time.Now()
				content, usage, statusCode, rawResponse, retryable, callErr := in.executeAISearchStreamRequest(parentCtx, endpoint, payload, requestBody)
				responseTrace := AISearchTrace{Round: *modelCalls, Attempt: attempt + 1, AttemptBudget: in.aiSearchRetryAttempts(), Stage: "普通模型响应", Endpoint: endpoint, Model: modelName, ModelCallCount: *modelCalls, ModelCallBudget: maxModelCalls, RemainingModelCalls: maxModelCalls - *modelCalls, Status: fmt.Sprintf("%d", statusCode), DurationMs: time.Since(attemptStartedAt).Milliseconds(), RequestBody: requestBody, ContentPreview: content, FullResponse: content, RawResponse: rawResponse, Error: errorString(callErr)}
				if call, ok := buildAIModelCallUsage(usage, modelName, *modelCalls, attempt+1); ok {
					appendAIModelCallUsage(&lastUsage, call)
					applyAIModelCallUsageToTrace(&responseTrace, lastUsage.ModelCalls[len(lastUsage.ModelCalls)-1])
				}
				recordTrace(responseTrace)
				attemptContent, attemptUsage, attemptEndpoint, attemptStatus, attemptRawResponse, attemptErr, attemptRetryable = content, usage, endpoint, statusCode, rawResponse, callErr, retryable
				modelCallHistory := append([]AIModelCallUsage(nil), lastUsage.ModelCalls...)
				addAISearchUsage(&lastUsage, attemptUsage)
				lastUsage.ModelCalls = modelCallHistory
				lastUsage.Trace = append([]AISearchTrace(nil), traceEvents...)
				if callErr == nil {
					lastUsage.Trace = append([]AISearchTrace(nil), traceEvents...)
					lastUsage.ModelCalls = append([]AIModelCallUsage(nil), modelCallHistory...)
					return content, lastUsage, statusCode, endpoint, requestBody, rawResponse, nil
				}
				if endpointIndex+1 >= len(endpoints) || !shouldTryAlternateAIEndpoint(statusCode) {
					break
				}
			}
			lastStatusCode = attemptStatus
			lastRawResponse = attemptRawResponse
			lastErr = attemptErr
			if !attemptRetryable {
				lastUsage.Trace = append([]AISearchTrace(nil), traceEvents...)
				lastUsage.ModelCalls = append([]AIModelCallUsage(nil), lastUsage.ModelCalls...)
				return attemptContent, lastUsage, lastStatusCode, attemptEndpoint, requestBody, lastRawResponse, lastErr
			}
		}
		if attempted == in.aiSearchRetryAttempts() && parentCtx.Err() == nil && !backupUsed && strings.TrimSpace(in.BackupModel) != "" && modelName != strings.TrimSpace(in.BackupModel) && IsAISearchSafetyPolicyError(lastErr) {
			modelName = strings.TrimSpace(in.BackupModel)
			backupUsed = true
			continue
		}
		break
	}

	lastUsage.ModelCalls = append([]AIModelCallUsage(nil), lastUsage.ModelCalls...)
	lastUsage.Trace = append([]AISearchTrace(nil), traceEvents...)
	return "", lastUsage, lastStatusCode, "", requestBody, lastRawResponse, lastErr
}

// executeAISearchStreamRequest 在全局并发配额内执行单次上游请求。
// retryable 表示上游明确拒绝且未开始结果流，可按状态退避重试。
func (in *ChatGptClient) executeAISearchStreamRequest(parentCtx context.Context, endpoint string, payload []byte, requestBody string) (string, AISearchUsage, int, string, bool, error) {
	requestStartedAt := time.Now()
	// 每次独立尝试最多等待首字超时；任务总时长由调用方 context 控制。
	ctx, cancel := context.WithTimeout(parentCtx, aiSearchFirstTokenTimeout)
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
		// 请求可能已被模型执行；响应丢失时无法证明不会重复付费。
		return "", AISearchUsage{}, 0, "", false, in.upstreamError("network", 0, "", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		rawResponse := string(rawBody)
		upstreamErr := in.upstreamError("http", resp.StatusCode, rawResponse, readErr)
		return "", AISearchUsage{}, resp.StatusCode, rawResponse, shouldRetryAISearchFailure(resp.StatusCode, upstreamErr), withAISearchRetryAfter(upstreamErr, resp)
	}

	firstTokenTimeout := aiSearchFirstTokenTimeout - time.Since(requestStartedAt)
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < firstTokenTimeout {
			firstTokenTimeout = remaining
		}
	}
	if firstTokenTimeout <= 0 {
		return "", AISearchUsage{}, resp.StatusCode, "", false, in.upstreamError("first_token", resp.StatusCode, "", errAISearchFirstTokenTimeout)
	}
	content, usage, rawResponse, err := readOpenAIStreamContentWithFirstTokenTimeout(resp.Body, firstTokenTimeout)
	if err != nil {
		upstreamErr := in.upstreamError("stream", resp.StatusCode, rawResponse, err)
		return "", usage, resp.StatusCode, rawResponse, shouldRetryAISearchFailure(resp.StatusCode, upstreamErr), withAISearchRetryAfter(upstreamErr, resp)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", usage, resp.StatusCode, rawResponse, false, errors.ErrTargetNoAnswer
	}
	return content, usage, resp.StatusCode, rawResponse, false, nil
}

func isRetryableAIHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests
}

func shouldTryAlternateAIEndpoint(status int) bool {
	return status == http.StatusNotFound
}

type aiSearchRetryAfterError struct {
	err   error
	delay time.Duration
}

func (e *aiSearchRetryAfterError) Error() string { return e.err.Error() }
func (e *aiSearchRetryAfterError) Unwrap() error { return e.err }

func withAISearchRetryAfter(err error, resp *http.Response) error {
	if err == nil || resp == nil || (resp.StatusCode != http.StatusTooManyRequests && !shouldRetryAISearchFailure(resp.StatusCode, err)) {
		return err
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return err
	}
	if seconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil && seconds >= 0 {
		if seconds > int64(time.Duration(1<<63-1)/time.Second) {
			return &aiSearchRetryAfterError{err: err, delay: time.Duration(1<<63 - 1)}
		}
		return &aiSearchRetryAfterError{err: err, delay: time.Duration(seconds) * time.Second}
	}
	if value[0] >= '0' && value[0] <= '9' && strings.Trim(value, "0123456789") == "" {
		return &aiSearchRetryAfterError{err: err, delay: time.Duration(1<<63 - 1)}
	}
	if retryAt, parseErr := http.ParseTime(value); parseErr == nil {
		delay := time.Until(retryAt)
		if delay < 0 {
			delay = 0
		}
		return &aiSearchRetryAfterError{err: err, delay: delay}
	}
	return err
}

func aiSearchRetryAfter(err error) (time.Duration, bool) {
	var retryErr *aiSearchRetryAfterError
	if stdErrors.As(err, &retryErr) {
		return retryErr.delay, true
	}
	return 0, false
}

func aiSearchRetryDelay(attempt int, retryErr error) time.Duration {
	delay := aiSearchRetryBaseDelay
	for step := 1; step < attempt && delay < aiSearchRetryMaxDelay; step++ {
		if delay > aiSearchRetryMaxDelay/2 {
			delay = aiSearchRetryMaxDelay
			break
		}
		delay *= 2
	}
	if serverDelay, ok := aiSearchRetryAfter(retryErr); ok {
		delay = serverDelay
	} else {
		delay += time.Duration(rand.Int63n(int64(delay/5) + 1))
		if delay > aiSearchRetryMaxDelay {
			delay = aiSearchRetryMaxDelay
		}
	}
	return delay
}

// Retry count comes from configuration; waits remain interruptible by the caller.
func sleepAISearchRetryBackoff(ctx context.Context, attempt int, retryErr error) bool {
	if ctx.Err() != nil {
		return false
	}
	delay := aiSearchRetryDelay(attempt, retryErr)
	if deadline, ok := ctx.Deadline(); ok && delay > time.Until(deadline) {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
	result, err := readAIResponsesStream(body, firstToken)
	return result.Content, result.Usage, result.Raw, err
}

func buildAISearchUsageFromResponses(responseUsage *openAIResponsesUsage) AISearchUsage {
	if responseUsage == nil {
		return AISearchUsage{}
	}
	usage := AISearchUsage{
		InputTokens:  responseUsage.InputTokens,
		OutputTokens: responseUsage.OutputTokens,
		TotalTokens:  responseUsage.TotalTokens,
	}
	if responseUsage.InputDetails != nil {
		usage.CachedInputTokens = normalizeAISearchCachedTokens(responseUsage.InputDetails.CachedTokens, responseUsage.InputTokens)
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

func parseAISearchAnswers(content string, options []string, questionType int) []string {
	trimmed := unwrapAISearchAnswerContent(content)
	if trimmed == "" {
		return nil
	}

	if answers, err := parseAISearchAnswersJSON(trimmed, options, questionType); err == nil {
		return answers
	}

	// 结构化 JSON：只要出现 answers（哪怕空数组）就以它为准，
	// 空答案必须保持 NO_ANSWER，不能退化成「把整段 JSON 当答案」。
	// 其余常见键（answer / 答案 / result / 判断…）来自模型偏离约定格式的响应：
	//   {"answer":"A","explanation":"..."}
	//   {"答案":"A","判断":"正确"}
	//   {"answer":"A","result":"正确"}
	if answers, structured := extractAISearchAnswersFromJSON(trimmed); structured {
		return normalizeAISearchAnswers(answers, options, questionType)
	}

	return normalizeAISearchAnswers([]string{trimmed}, options, questionType)
}

// parseAISearchAnswersJSON accepts only a complete, recognized answer JSON
// document. Transport/model wrappers are removed by unwrapAISearchAnswerContent
// before decoding; arbitrary prose or partial JSON is rejected.
func parseAISearchAnswersJSON(content string, options []string, questionType int) ([]string, error) {
	trimmed := unwrapAISearchAnswerContent(content)
	if trimmed == "" {
		return nil, fmt.Errorf("答案JSON为空")
	}
	var jsonArray []string
	if err := json.Unmarshal([]byte(trimmed), &jsonArray); err == nil {
		return normalizeAISearchAnswers(jsonArray, options, questionType), nil
	}
	answers, structured := extractAISearchAnswersFromJSON(trimmed)
	if !structured {
		return nil, fmt.Errorf("答案JSON缺少 answers 字段")
	}
	return normalizeAISearchAnswers(answers, options, questionType), nil
}

// unwrapAISearchAnswerContent handles only paired wrappers observed in model
// responses. It never searches for an inner brace or silently accepts a
// truncated wrapper, preventing prose/partial JSON from becoming an answer.
func unwrapAISearchAnswerContent(content string) string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	for depth := 0; depth < 3; depth++ {
		if strings.HasPrefix(trimmed, "```") {
			lineEnd := strings.IndexByte(trimmed, '\n')
			if lineEnd < 0 {
				return trimmed
			}
			if !strings.HasSuffix(strings.TrimSpace(trimmed), "```") {
				return trimmed
			}
			body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(trimmed), "```"))
			trimmed = strings.TrimSpace(body[lineEnd+1:])
			continue
		}
		unwrapped := false
		for _, tag := range []string{"escape", "json", "answer"} {
			open, close := "<"+tag+">", "</"+tag+">"
			if len(trimmed) >= len(open)+len(close) && strings.EqualFold(trimmed[:len(open)], open) {
				if !strings.EqualFold(trimmed[len(trimmed)-len(close):], close) {
					return trimmed
				}
				trimmed = strings.TrimSpace(trimmed[len(open) : len(trimmed)-len(close)])
				unwrapped = true
				break
			}
		}
		if !unwrapped {
			break
		}
	}
	return strings.TrimSpace(trimmed)
}

// stripAISearchCodeFence 去掉模型偶尔包上的 ```json ... ``` 围栏。
func stripAISearchCodeFence(content string) string {
	return unwrapAISearchAnswerContent(content)
}

// aiSearchLooseAnswerKeys 是模型偏离约定格式时仍可接受的答案键（按优先级）。
var aiSearchLooseAnswerKeys = []string{"answers", "answer", "答案", "正确答案", "result", "判断", "选择"}

// extractAISearchAnswersFromJSON 解析 JSON 对象里的答案。
//
// structured=true 表示「这是结构化答案 JSON」——即使 answers 是空数组，
// 也必须按空答案处理（NO_ANSWER），不能回退成把整段 JSON 当答案。
func extractAISearchAnswersFromJSON(content string) (answers []string, structured bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, false
	}
	if value, ok := raw["answers"]; ok {
		// answers is authoritative, including null, empty, or unsupported values;
		// never fall through to a conflicting singular answer field.
		return decodeAISearchAnswerValue(value), true
	}
	for _, key := range aiSearchLooseAnswerKeys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if decoded := decodeAISearchAnswerValue(value); len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, structured
}

// decodeAISearchAnswerValue 把答案值解析成字符串列表（兼容字符串与字符串数组）。
func decodeAISearchAnswerValue(raw json.RawMessage) []string {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if trimmed := strings.TrimSpace(single); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		answers := make([]string, 0, len(list))
		for _, item := range list {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				answers = append(answers, trimmed)
			}
		}
		return answers
	}
	return nil
}

func normalizeAISearchAnswers(rawAnswers []string, options []string, questionType int) []string {
	seen := make(map[string]struct{}, len(rawAnswers))
	answers := make([]string, 0, len(rawAnswers))
	// 非客观题（填空题/连线题/简答题/阅读理解）必须保留重复答案：
	// 多个空或多个连线点可能是同一个答案文本，去重会让答案数与空数对不上。
	preserveRepeatedAnswers := util.AllowsRepeatedAnswers(questionType)
	for _, rawAnswer := range rawAnswers {
		resolved := resolveAISearchAnswer(rawAnswer, options, questionType)
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

func resolveAISearchAnswer(answer string, options []string, questionType int) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return ""
	}

	// 选项先按「最终响应里的形态」归一化：去掉 "A." 前缀、判断题的 对/错 → 正确/错误。
	// 否则选项是 "A. 正确" 时，模型返回「对」会因为选项带前缀而匹配失败，
	// 随后 FillAnswerResponse 用归一化选项（"正确"/"错误"）又匹配不上 → 空答案。
	// 线上实测：判断题 211 次里 181 次返回空答案。
	normalizedOptions := util.FormatOptions(options, questionType)
	normalizedAnswer := util.FormatString(trimmed)

	if mapped := matchAISearchOptionByLabel(normalizedAnswer, normalizedOptions); mapped != "" {
		return mapped
	}
	if mapped := matchAISearchJudgementOption(normalizedAnswer, normalizedOptions); mapped != "" {
		return mapped
	}
	if mapped := matchAISearchOptionByText(normalizedAnswer, normalizedOptions); mapped != "" {
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
	// 判定集中在 pkg/util，AI 兜底链路复用同一套，避免两条链路答案有效性不一致。
	return util.JudgementPolarity(value)
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
