package search

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

// CompletionImage 是一次「辅助判断」型模型调用里随文本一起提交的图片。
//
// 它与搜题链路的图片附件走**完全相同的准备流程**（prepareAISearchImageAttachment：
// 透明底合成白底、过小的图适度放大），也走相同的 Responses 端点探测 / SSE 解析 /
// 重试预算 / 并发配额 —— 这里刻意不新建一套 HTTP 客户端与密钥管理。
type CompletionImage struct {
	// Name 是可读文件名，仅用于日志，不参与请求内容。
	Name string
	// ContentType 由本地解码结果决定（image/png 或 image/jpeg），不信任文件名后缀。
	ContentType string
	// Body 是原始图片字节。
	Body []byte
}

// CompleteWithImages 复用搜题模型客户端执行一次单轮带图调用，返回模型纯文本输出。
//
// 为什么需要它：搜题链路（SearchAnswerWithUsage）把模型输出当作 `{"answers":[...]}`
// 解析，并会按题型做答案归一化；评价返现要的是一段结构化 JSON（stars/contentScore/
// confidence/rationale），过答案解析只会把它拆坏。这里复用**传输层**、跳过**题型层**。
//
// 预算语义：只允许一轮模型调用（maxModelCalls=1），重试次数沿用
// setting `ai_search_max_retries`（与搜题同一配置），避免两层重试相乘。
func (in *ChatGptClient) CompleteWithImages(
	ctx context.Context,
	systemPrompt string,
	userText string,
	images []CompletionImage,
) (string, AISearchUsage, error) {
	usage := AISearchUsage{Route: AISearchRouteNormal}
	if in == nil || !in.Enable {
		return "", usage, fmt.Errorf("模型客户端未启用")
	}
	if strings.TrimSpace(in.BaseURL) == "" || strings.TrimSpace(in.APIKey) == "" || strings.TrimSpace(in.Model) == "" {
		return "", usage, fmt.Errorf("模型地址、密钥或模型名未配置完整")
	}
	if strings.TrimSpace(userText) == "" {
		return "", usage, fmt.Errorf("模型请求内容为空")
	}
	endpoints := buildAIResponsesEndpoints(in.BaseURL)
	if len(endpoints) == 0 {
		return "", usage, fmt.Errorf("模型地址无效")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := withAITotalTimeout(ctx)
	defer cancel()

	parts := make([]openAIContentPart, 0, len(images)+1)
	parts = append(parts, openAIContentPart{Type: "text", Text: userText})
	for _, image := range images {
		if len(image.Body) == 0 {
			continue
		}
		contentType := strings.TrimSpace(image.ContentType)
		if contentType == "" {
			contentType = "image/png"
		}
		preparedBody, preparedContentType := prepareAISearchImageAttachment(image.Body, contentType)
		parts = append(parts, openAIContentPart{
			Type: "image_url",
			ImageURL: &openAIImageURLPart{
				URL: "data:" + preparedContentType + ";base64," + base64.StdEncoding.EncodeToString(preparedBody),
			},
		})
	}

	modelCalls := 0
	const maxModelCalls = 1
	content, streamUsage, _, endpoint, _, rawResponse, err := in.requestStreamAnswerDetailed(
		ctx, endpoints, strings.TrimSpace(systemPrompt), parts, &modelCalls, maxModelCalls)
	if err != nil {
		// 原始响应只写私有日志：里面可能有用户截图内容与上游报错细节。
		logger.SysError(logger.RedactSecrets(fmt.Sprintf(
			"辅助模型调用失败 model=%s endpoint=%s: %v", strings.TrimSpace(in.Model), endpoint, err), in.APIKey))
		usage = streamUsage
		usage.Route = AISearchRouteNormal
		return "", usage, err
	}
	usage = streamUsage
	usage.Route = AISearchRouteNormal
	if strings.TrimSpace(content) == "" && strings.TrimSpace(rawResponse) == "" {
		return "", usage, fmt.Errorf("模型返回空内容")
	}
	return content, usage, nil
}
