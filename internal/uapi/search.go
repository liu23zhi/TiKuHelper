package uapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultSearchEnginesURL   = "https://uapis.cn/api/v1/search/engines"
	DefaultSearchAggregateURL = "https://uapis.cn/api/v1/search/aggregate"
	searchTotalTimeout        = 60 * time.Second
)

type SearchEngine struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Available   bool     `json:"available"`
	Version     string   `json:"version"`
	Features    []string `json:"features"`
}

type SearchLimits struct {
	Default int `json:"default"`
	Max     int `json:"max"`
}

type SearchEnginesResponse struct {
	Engine              SearchEngine `json:"engine"`
	Limits              SearchLimits `json:"limits"`
	SupportedParameters []string     `json:"supported_parameters"`
	RawJSON             string       `json:"-"`
}

// SearchAggregateRequest 仅包含文档列出的请求字段，返回元数据中的 limit/page/timeout_ms 不作为入参。
type SearchAggregateRequest struct {
	Query     string `json:"query"`
	Site      string `json:"site,omitempty"`
	FileType  string `json:"filetype,omitempty"`
	FetchFull bool   `json:"fetch_full,omitempty"`
	Sort      string `json:"sort,omitempty"`
	TimeRange string `json:"time_range,omitempty"`
}

type SearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Domain      string  `json:"domain"`
	Source      string  `json:"source"`
	Position    int     `json:"position"`
	Score       float64 `json:"score"`
	PublishTime string  `json:"publish_time"`
}

type SearchSource struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	ResultCount     int    `json:"result_count"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	FirstResultHost string `json:"first_result_host"`
}

type SearchRequestParams struct {
	Query     string `json:"query"`
	Limit     int    `json:"limit"`
	Page      int    `json:"page"`
	TimeoutMS int64  `json:"timeout_ms"`
	Sort      string `json:"sort"`
}

type SearchMetadata struct {
	RequestParams  SearchRequestParams `json:"request_params"`
	DedupeRemoved  int                 `json:"dedupe_removed"`
	RerankApplied  bool                `json:"rerank_applied"`
	ContentFetched int                 `json:"content_fetched"`
}

type SearchAggregateResponse struct {
	Query         string         `json:"query"`
	TotalResults  int            `json:"total_results"`
	Results       []SearchResult `json:"results"`
	Sources       []SearchSource `json:"sources"`
	ProcessTimeMS int64          `json:"process_time_ms"`
	Metadata      SearchMetadata `json:"metadata"`
	// 保留完整成功响应，包括 fetch_full 时文档没有定义名称的扩展字段。
	RawJSON string `json:"-"`
}

func (c *Client) SetSearchEnginesURL(url string) *Client {
	if c != nil {
		c.searchEnginesURL = strings.TrimSpace(url)
	}
	return c
}

func (c *Client) SetSearchAggregateURL(url string) *Client {
	if c != nil {
		c.searchAggregateURL = strings.TrimSpace(url)
	}
	return c
}

func (c *Client) GetSearchEngines(ctx context.Context) (SearchEnginesResponse, error) {
	if err := c.ensureSearchReady(); err != nil {
		return SearchEnginesResponse{}, err
	}
	ctx, cancel := c.searchContext(ctx)
	defer cancel()
	address := c.searchEnginesURL
	if address == "" {
		address = DefaultSearchEnginesURL
	}
	response, err := c.get(ctx, address, nil)
	if err != nil {
		return SearchEnginesResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return SearchEnginesResponse{}, err
	}
	var result SearchEnginesResponse
	if err := decodeSearchResponse(response.Body(), &result, "engine", "limits", "supported_parameters"); err != nil {
		return SearchEnginesResponse{}, err
	}
	if strings.TrimSpace(result.Engine.Name) == "" {
		return SearchEnginesResponse{}, fmt.Errorf("%w: 搜索引擎响应缺少 engine.name", ErrUpstream)
	}
	result.RawJSON = string(response.Body())
	return result, nil
}

func (c *Client) SearchAggregate(ctx context.Context, input SearchAggregateRequest) (SearchAggregateResponse, error) {
	if err := c.ensureSearchReady(); err != nil {
		return SearchAggregateResponse{}, err
	}
	normalized, err := normalizeSearchAggregateRequest(input)
	if err != nil {
		return SearchAggregateResponse{}, err
	}
	ctx, cancel := c.searchContext(ctx)
	defer cancel()
	address := c.searchAggregateURL
	if address == "" {
		address = DefaultSearchAggregateURL
	}
	response, err := c.request(ctx, http.MethodPost, address, nil, normalized)
	if err != nil {
		return SearchAggregateResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return SearchAggregateResponse{}, err
	}
	var result SearchAggregateResponse
	if err := decodeSearchResponse(response.Body(), &result, "query", "total_results", "results"); err != nil {
		return SearchAggregateResponse{}, err
	}
	if strings.TrimSpace(result.Query) == "" {
		return SearchAggregateResponse{}, fmt.Errorf("%w: 搜索响应缺少 query", ErrUpstream)
	}
	result.RawJSON = string(response.Body())
	return result, nil
}

func (c *Client) ensureSearchReady() error {
	if err := c.ensureReady(); err != nil {
		return err
	}
	if !strings.HasPrefix(c.apiKey, "uapi-") || len(c.apiKey) <= len("uapi-") || strings.ContainsFunc(c.apiKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("%w: UAPI 密钥必须以 uapi- 开头且不能包含空白", ErrUnauthorized)
	}
	return nil
}

// 搜索总时限包含全部重试与 Retry-After，避免一次限流让调用无限等待。
func (c *Client) searchContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, searchTotalTimeout)
}

func normalizeSearchAggregateRequest(input SearchAggregateRequest) (SearchAggregateRequest, error) {
	input.Query = strings.TrimSpace(input.Query)
	input.Site = strings.TrimSpace(input.Site)
	input.FileType = strings.TrimSpace(input.FileType)
	input.Sort = strings.TrimSpace(input.Sort)
	input.TimeRange = strings.TrimSpace(input.TimeRange)
	if input.Query == "" {
		return input, fmt.Errorf("%w: query 不能为空", ErrInvalidArgument)
	}
	if strings.HasPrefix(strings.ToLower(input.Site), "site:") {
		return input, fmt.Errorf("%w: site 不应包含 site: 前缀", ErrInvalidArgument)
	}
	if input.FileType != "" {
		input.FileType = strings.ToLower(input.FileType)
		switch strings.ToLower(input.FileType) {
		case "pdf", "doc", "docx", "ppt", "pptx", "xls", "xlsx", "txt":
		default:
			return input, fmt.Errorf("%w: 不支持的 filetype", ErrInvalidArgument)
		}
	}
	if input.Sort != "" && input.Sort != "relevance" && input.Sort != "date" {
		return input, fmt.Errorf("%w: sort 只支持 relevance 或 date", ErrInvalidArgument)
	}
	if input.TimeRange != "" && input.TimeRange != "day" && input.TimeRange != "week" && input.TimeRange != "month" && input.TimeRange != "year" {
		return input, fmt.Errorf("%w: time_range 只支持 day、week、month 或 year", ErrInvalidArgument)
	}
	return input, nil
}

func decodeSearchResponse(body []byte, result interface{}, requiredFields ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return fmt.Errorf("%w: 解析搜索响应失败: %w", ErrUpstream, err)
	}
	for _, name := range requiredFields {
		field, exists := fields[name]
		if !exists || string(field) == "null" {
			return fmt.Errorf("%w: 搜索响应缺少 %s", ErrUpstream, name)
		}
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("%w: 解析搜索响应失败: %w", ErrUpstream, err)
	}
	return nil
}
