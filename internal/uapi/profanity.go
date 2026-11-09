package uapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultProfanityCheckURL 敏感词检测接口完整地址
	DefaultProfanityCheckURL = "https://uapis.cn/api/v1/text/profanitycheck"
	// profanityStatusForbidden 是上游判定「命中敏感词」的唯一状态值。
	profanityStatusForbidden = "forbidden"
	// profanityTotalTimeout 单次检测总时限（含内部重试）。
	//
	// 检测是旁路能力：宁可放弃一次检测，也不能让后台检测任务长期堆积。
	profanityTotalTimeout = 15 * time.Second
)

// ProfanityCheckRequest 仅包含文档列出的请求字段。
type ProfanityCheckRequest struct {
	Text string `json:"text"`
}

// ProfanityCheckResponse 敏感词检测 200 响应。
//
// 上游把「是否命中」编码在 status 里：forbidden 表示命中，其它任何值都表示未命中。
// masked_text 是上游给出的屏蔽后文本，本项目只把它当**参考值**落库，
// 绝不替换答题返回的原文。
type ProfanityCheckResponse struct {
	Status         string   `json:"status"`
	OriginalText   string   `json:"original_text"`
	MaskedText     string   `json:"masked_text"`
	ForbiddenWords []string `json:"forbidden_words"`
	// RawJSON 本次 200 响应的完整原始 body（原样保留，不参与序列化）。
	RawJSON string `json:"-"`
}

// Forbidden 报告本次结果是否命中敏感词。
//
// 只有 status == "forbidden" 才算命中：上游未来若新增状态值（例如 pending），
// 一律按未命中处理，避免把正常文本误标成敏感。
func (r ProfanityCheckResponse) Forbidden() bool {
	return strings.EqualFold(strings.TrimSpace(r.Status), profanityStatusForbidden)
}

// SetProfanityCheckURL 覆盖敏感词检测接口地址（默认 DefaultProfanityCheckURL），仅用于测试/代理
func (c *Client) SetProfanityCheckURL(address string) *Client {
	if c == nil {
		return nil
	}
	c.profanityCheckURL = strings.TrimSpace(address)
	return c
}

func (c *Client) profanityCheckAddress() string {
	if c != nil && c.profanityCheckURL != "" {
		return c.profanityCheckURL
	}
	return DefaultProfanityCheckURL
}

// CheckProfanity 调用 uapis.cn 敏感词检测。
//
// 提交型 POST 不重放（见 internal/uapi/client.go 的 request），
// 因此这里显式给一个总时限，避免后台任务被上游拖死。
// 所有错误都只是「本次没检测成」，调用方必须按旁路失败处理。
func (c *Client) CheckProfanity(ctx context.Context, input ProfanityCheckRequest) (ProfanityCheckResponse, error) {
	if err := c.ensureProfanityReady(); err != nil {
		return ProfanityCheckResponse{}, err
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		// 空文本没有检测意义，本地直接返回未命中，不消耗积分。
		return ProfanityCheckResponse{Status: "clean", OriginalText: ""}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, profanityTotalTimeout)
	defer cancel()

	response, err := c.request(ctx, http.MethodPost, c.profanityCheckAddress(), nil, ProfanityCheckRequest{Text: text})
	if err != nil {
		return ProfanityCheckResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return ProfanityCheckResponse{}, err
	}
	result, err := ParseProfanityCheck(response.Body())
	if err != nil {
		return ProfanityCheckResponse{}, err
	}
	return result, nil
}

// ensureProfanityReady 校验密钥形态，避免明显无效的密钥白跑一次请求。
func (c *Client) ensureProfanityReady() error {
	if err := c.ensureReady(); err != nil {
		return err
	}
	if !strings.HasPrefix(c.apiKey, "uapi-") || len(c.apiKey) <= len("uapi-") {
		return fmt.Errorf("%w: UAPI 密钥必须以 uapi- 开头", ErrUnauthorized)
	}
	return nil
}

// ParseProfanityCheck 解析 200 响应，同时保留完整原始 body。
//
// 字段用 map 逐项容错读取：上游未列出的字段不影响解析，
// forbidden_words 缺失或为 null 时返回空切片而不是 nil（便于 JSON 落库）。
func ParseProfanityCheck(body []byte) (ProfanityCheckResponse, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return ProfanityCheckResponse{}, fmt.Errorf("%w: 解析敏感词检测响应失败: %w", ErrUpstream, err)
	}
	result := ProfanityCheckResponse{
		Status:         rawString(fields["status"]),
		OriginalText:   rawString(fields["original_text"]),
		MaskedText:     rawString(fields["masked_text"]),
		ForbiddenWords: rawStringSlice(fields["forbidden_words"]),
		RawJSON:        string(body),
	}
	if strings.TrimSpace(result.Status) == "" {
		return ProfanityCheckResponse{}, fmt.Errorf("%w: 敏感词检测响应缺少 status", ErrUpstream)
	}
	return result, nil
}

// rawStringSlice 容错解析字符串数组：非数组或元素非字符串时跳过该元素。
func rawStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return []string{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := rawString(value); text != "" {
			result = append(result, text)
		}
	}
	return result
}
