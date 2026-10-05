package search

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-resty/resty/v2"
	"github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// ChatGptClient AI 搜题客户端
type ChatGptClient struct {
	Enable      bool
	APIKey      string
	BaseURL     string
	Model       string
	Prompt      string
	Temperature float64
	TopP        float64
}

type AISearchUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type AISearchDebugResult struct {
	Endpoint    string
	RequestBody string
	RawResponse string
	Usage       AISearchUsage
}

type openAIChatCompletionRequest struct {
	Model         string               `json:"model"`
	Stream        bool                 `json:"stream"`
	Temperature   float64              `json:"temperature"`
	TopP          float64              `json:"top_p"`
	Messages      []openAIChatMessage  `json:"messages"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
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
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

type aiSearchAnswerPayload struct {
	Answers []string `json:"answers"`
}

var aiSearchImageURLPattern = regexp.MustCompile(`https?://[^\s,，]+`)

func defaultAISearchPrompt() string {
	return strings.TrimSpace(`
你是一个用于题库检索结果补充的答题助手。
请只返回 JSON，不要输出 markdown、代码块或额外解释。
输出格式固定为：{"answers":["答案1","答案2"]}。
单选题或判断题只返回 1 个最可能答案，优先返回与选项完全一致的文本；判断题请返回“正确”或“错误”。
多选题按最终答案顺序返回多个答案文本；填空题按每空一个答案返回。
如果信息不足，请返回：{"answers":["不确定"]}。
保持答案稳定、简洁，并兼容题库系统后续处理。
	`)
}

func effectiveAISearchPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return defaultAISearchPrompt()
	}
	return trimmed
}

// getHTTPClient 获取HTTP客户端
func (in *ChatGptClient) getHTTPClient() *resty.Client {
	return resty.NewWithClient(util.NewSafeHTTPClient(180 * time.Second))
}

// SearchAnswer 使用 OpenAI 兼容流式接口聚合答案
func (in *ChatGptClient) SearchAnswer(req model.SearchRequest) (answer [][]string, err error) {
	answer, _, err = in.SearchAnswerWithUsage(req)
	return answer, err
}

func (in *ChatGptClient) SearchAnswerDebug(req model.SearchRequest) (answer [][]string, debug AISearchDebugResult, err error) {
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
	userPromptParts := buildAISearchPromptParts(req, in.getHTTPClient().GetClient())
	endpoints := buildAIChatCompletionEndpoints(baseURL)
	for _, endpoint := range endpoints {
		content, usage, statusCode, requestBody, rawResponse, callErr := in.requestStreamAnswerDetailed(endpoint, systemPrompt, userPromptParts)
		debug = AISearchDebugResult{
			Endpoint:    endpoint,
			RequestBody: requestBody,
			RawResponse: rawResponse,
			Usage:       usage,
		}
		if callErr == nil {
			parsedAnswers := parseAISearchAnswers(content, req.Options)
			if len(parsedAnswers) == 0 {
				return nil, debug, errors.ErrTargetNoAnswer
			}
			return [][]string{parsedAnswers}, debug, nil
		}
		if statusCode == http.StatusNotFound {
			continue
		}
		return nil, debug, callErr
	}

	return nil, debug, errors.ErrTargetNoAnswer
}

func (in *ChatGptClient) SearchAnswerWithUsage(req model.SearchRequest) (answer [][]string, usage AISearchUsage, err error) {
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
	userPromptParts := buildAISearchPromptParts(req, in.getHTTPClient().GetClient())
	endpoints := buildAIChatCompletionEndpoints(baseURL)
	for _, endpoint := range endpoints {
		content, streamUsage, statusCode, callErr := in.requestStreamAnswer(endpoint, systemPrompt, userPromptParts)
		if callErr == nil {
			parsedAnswers := parseAISearchAnswers(content, req.Options)
			if len(parsedAnswers) == 0 {
				return nil, AISearchUsage{}, errors.ErrTargetNoAnswer
			}
			return [][]string{parsedAnswers}, streamUsage, nil
		}
		if statusCode == http.StatusNotFound {
			continue
		}
		return nil, AISearchUsage{}, callErr
	}

	return nil, AISearchUsage{}, errors.ErrTargetNoAnswer
}

func buildAISearchPrompt(req model.SearchRequest) string {
	var builder strings.Builder
	builder.WriteString("请根据系统规则返回答案 JSON。")
	builder.WriteString("\n题型：")
	builder.WriteString(resolveAISearchQuestionTypeLabel(req.Type))
	builder.WriteString("\n题目：")
	builder.WriteString(strings.TrimSpace(req.Question))
	if req.Type == 2 {
		builder.WriteString("\n题型补充说明：该题型在调用侧同时承载填空题、连线题、匹配题；如果题目要求建立对应关系，请按每组对应关系依次返回 answers 数组。")
	}
	if req.Type == 4 {
		builder.WriteString("\n题型补充说明：该题型在调用侧可能是简答题，也可能是阅读理解或其他文本作答题；请直接返回最终文本答案。")
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
		builder.WriteString("\n选项：")
		for idx, option := range util.FormatOptions(req.Options, req.Type) {
			builder.WriteString(fmt.Sprintf("\n%c. %s", 'A'+idx, strings.TrimSpace(option)))
		}
	}
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
		return "简答题/阅读理解题"
	case -4:
		return "大作业"
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
	if httpClient == nil {
		return parts
	}
	for _, imageURL := range extractAISearchImageURLs(req) {
		dataURL, ok := downloadAISearchImageAsDataURL(httpClient, imageURL)
		if !ok {
			continue
		}
		parts = append(parts, openAIContentPart{
			Type: "image_url",
			ImageURL: &openAIImageURLPart{
				URL: dataURL,
			},
		})
	}
	return parts
}

func extractAISearchImageURLs(req model.SearchRequest) []string {
	seen := make(map[string]struct{})
	urls := make([]string, 0)
	appendMatches := func(value string) {
		for _, match := range aiSearchImageURLPattern.FindAllString(value, -1) {
			trimmed := strings.TrimSpace(strings.TrimRight(match, ".;:!?)]}，。；：！？》」』、"))
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

func downloadAISearchImageAsDataURL(httpClient *http.Client, imageURL string) (string, bool) {
	if httpClient == nil {
		return "", false
	}
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	resp, err := httpClient.Get(imageURL)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", false
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" || !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil || len(body) == 0 {
		return "", false
	}
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), true
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

func (in *ChatGptClient) requestStreamAnswer(endpoint string, systemPrompt string, userPromptParts []openAIContentPart) (string, AISearchUsage, int, error) {
	content, usage, statusCode, _, _, err := in.requestStreamAnswerDetailed(endpoint, systemPrompt, userPromptParts)
	return content, usage, statusCode, err
}

func (in *ChatGptClient) requestStreamAnswerDetailed(endpoint string, systemPrompt string, userPromptParts []openAIContentPart) (string, AISearchUsage, int, string, string, error) {
	payload, err := json.Marshal(openAIChatCompletionRequest{
		Model:       strings.TrimSpace(in.Model),
		Stream:      true,
		Temperature: 0,
		TopP:        0,
		Messages: []openAIChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPromptParts},
		},
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
	})
	if err != nil {
		return "", AISearchUsage{}, 0, "", "", errors.ErrParserJSON
	}

	requestBody := string(payload)
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", AISearchUsage{}, 0, requestBody, "", errors.ErrTargetServerError
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(in.APIKey))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := in.getHTTPClient().GetClient().Do(httpReq)
	if err != nil {
		return "", AISearchUsage{}, 0, requestBody, "", errors.ErrTargetServerError
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return "", AISearchUsage{}, resp.StatusCode, requestBody, "", errors.ErrTargetServerError
	}
	rawResponse := string(rawBody)

	if resp.StatusCode == http.StatusNotFound {
		return "", AISearchUsage{}, resp.StatusCode, requestBody, rawResponse, errors.ErrTargetNoAnswer
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", AISearchUsage{}, resp.StatusCode, requestBody, rawResponse, errors.ErrTargetServerError
	}

	content, usage, err := readOpenAIStreamContent(strings.NewReader(rawResponse))
	if err != nil {
		return "", AISearchUsage{}, resp.StatusCode, requestBody, rawResponse, errors.ErrTargetServerError
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", usage, resp.StatusCode, requestBody, rawResponse, errors.ErrTargetNoAnswer
	}
	return content, usage, resp.StatusCode, requestBody, rawResponse, nil
}

func readOpenAIStreamContent(body io.Reader) (string, AISearchUsage, error) {
	scanner := bufio.NewScanner(body)
	var builder strings.Builder
	var usage AISearchUsage
	for scanner.Scan() {
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
			return "", AISearchUsage{}, err
		}
		for _, choice := range chunk.Choices {
			builder.WriteString(choice.Delta.Content)
		}
		if chunk.Usage != nil {
			usage = AISearchUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", AISearchUsage{}, err
	}
	return builder.String(), usage, nil
}

func parseAISearchAnswers(content string, options []string) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	var payload aiSearchAnswerPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		return normalizeAISearchAnswers(payload.Answers, options)
	}

	var jsonArray []string
	if err := json.Unmarshal([]byte(trimmed), &jsonArray); err == nil {
		return normalizeAISearchAnswers(jsonArray, options)
	}

	return normalizeAISearchAnswers([]string{trimmed}, options)
}

func normalizeAISearchAnswers(rawAnswers []string, options []string) []string {
	seen := make(map[string]struct{}, len(rawAnswers))
	answers := make([]string, 0, len(rawAnswers))
	for _, rawAnswer := range rawAnswers {
		resolved := resolveAISearchAnswer(rawAnswer, options)
		if resolved == "" {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
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
