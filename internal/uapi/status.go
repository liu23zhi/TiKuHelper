package uapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const DefaultStatusUsageURL = "https://uapis.cn/api/v1/status/usage"

type StatusUsageEndpoint struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

type StatusUsageUnaggregated struct {
	Count     int64  `json:"count"`
	OldestLog string `json:"oldest_log"`
}

// StatusUsageResponse 是 /status/usage 的两种响应形态的兼容结构。
// RawJSON 保留文档未列出的统计字段，便于管理端后续展示或排查。
type StatusUsageResponse struct {
	Path         string                  `json:"path,omitempty"`
	Count        int64                   `json:"count,omitempty"`
	Endpoints    []StatusUsageEndpoint   `json:"endpoints,omitempty"`
	Unaggregated StatusUsageUnaggregated `json:"unaggregated,omitempty"`
	RawJSON      string                  `json:"-"`
}

func (c *Client) SetStatusUsageURL(url string) *Client {
	if c != nil {
		c.statusUsageURL = strings.TrimSpace(url)
	}
	return c
}

// GetStatusUsage 查询 UAPI 端点使用统计；path 为空时查询全部端点。
func (c *Client) GetStatusUsage(ctx context.Context, path string) (StatusUsageResponse, error) {
	if err := c.ensureReady(); err != nil {
		return StatusUsageResponse{}, err
	}
	query := map[string]string{}
	path = strings.TrimSpace(path)
	if path != "" {
		query["path"] = path
	}
	address := c.statusUsageURL
	if address == "" {
		address = DefaultStatusUsageURL
	}
	response, err := c.get(ctx, address, query)
	if err != nil {
		return StatusUsageResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return StatusUsageResponse{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body(), &fields); err != nil {
		return StatusUsageResponse{}, fmt.Errorf("%w: 解析端点使用统计响应失败: %w", ErrUpstream, err)
	}
	var result StatusUsageResponse
	if err := json.Unmarshal(response.Body(), &result); err != nil {
		return StatusUsageResponse{}, fmt.Errorf("%w: 解析端点使用统计响应失败: %w", ErrUpstream, err)
	}
	if path != "" {
		if strings.TrimSpace(result.Path) == "" || result.Count < 0 || fields["count"] == nil {
			return StatusUsageResponse{}, fmt.Errorf("%w: 端点使用统计响应缺少 path/count", ErrUpstream)
		}
	} else if fields["endpoints"] == nil {
		return StatusUsageResponse{}, fmt.Errorf("%w: 端点使用统计响应缺少 endpoints", ErrUpstream)
	}
	result.RawJSON = string(response.Body())
	return result, nil
}
