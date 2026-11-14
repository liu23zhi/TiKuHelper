package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 本文件钉住判据 S4（task_doc/138 §4.4）：
//
//	摘要与序列**两个端点都**必须返回分桶。
//
// 只改一个就必红的理由不是「对称性洁癖」：摘要端点给「搜题接口现在好不好」，
// 序列端点给「什么时候开始不好的」。只给一边会出现两种自相矛盾的界面：
//   - 只有摘要带分桶 → 曲线全红但看不出是哪个接口族
//   - 只有序列带分桶 → 摘要说健康、曲线里全是搜题的失败点
//
// 同时钉住「公开端点的分桶不给 failedRequests」这条暴露面取舍。

// seededEndpointSampler 造一个「search 有流量、other 也有流量」的采样器。
func seededEndpointSampler(t *testing.T, minutes int) *service.ServiceStatusSampler {
	t.Helper()
	fixed := time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)
	ring := service.NewServiceStatusRing()
	sampler := service.NewServiceStatusSampler(ring, func() time.Time { return fixed }, nil)
	for i := 0; i < minutes; i++ {
		ring.Append(service.StatusPoint{
			Minute:          fixed.Unix()/60 + int64(i),
			TotalRequests:   10,
			SuccessRequests: 8,
			FailedRequests:  2,
			AvgLatencyMs:    100,
			Endpoints: service.StatusCountersByEndpoint{
				Search: service.EndpointCounters{
					TotalRequests:   6,
					SuccessRequests: 5,
					FailedRequests:  1,
					AvgLatencyMs:    80,
					Samples:         1,
				},
				Other: service.EndpointCounters{
					TotalRequests:   4,
					SuccessRequests: 3,
					FailedRequests:  1,
					AvgLatencyMs:    130,
					Samples:         1,
				},
			},
		})
	}
	return sampler
}

// TestServiceStatusSummaryReturnsEndpoints 是 S4 的摘要面。
func TestServiceStatusSummaryReturnsEndpoints(t *testing.T) {
	sampler := seededEndpointSampler(t, 3)
	body := buildServiceStatusSummaryResponse(sampler)

	// 3 个分钟里每分钟 6 次 search ⇒ 窗内 18。
	if body.Endpoints.Search.TotalRequests != 18 {
		t.Fatalf("摘要 search 桶请求数期望 18（3 分钟 × 6），got=%d", body.Endpoints.Search.TotalRequests)
	}
	if body.Endpoints.Search.SuccessRequests != 15 {
		t.Fatalf("摘要 search 桶成功数期望 15，got=%d", body.Endpoints.Search.SuccessRequests)
	}
	if body.Endpoints.Search.AvgLatencyMs != 80 {
		t.Fatalf("摘要 search 桶平均延迟期望 80（每分钟都是 80，加权后仍是 80），got=%d",
			body.Endpoints.Search.AvgLatencyMs)
	}
	// samples 在**整个保留序列**上数（3 个点）。
	if body.Endpoints.Search.Samples != 3 {
		t.Fatalf("摘要 search 桶 samples 期望 3，got=%d", body.Endpoints.Search.Samples)
	}
	if body.Endpoints.Other.TotalRequests != 12 {
		t.Fatalf("摘要 other 桶请求数期望 12（3 分钟 × 4），got=%d", body.Endpoints.Other.TotalRequests)
	}

	// 分桶之和必须等于顶层总数 —— 分桶是加法信息，不能凭空多/少。
	if sum := body.Endpoints.Search.TotalRequests + body.Endpoints.Other.TotalRequests; sum != body.TotalRequests {
		t.Fatalf("两桶之和 %d 必须等于顶层 totalRequests %d", sum, body.TotalRequests)
	}
}

// TestServiceStatusSeriesReturnsEndpoints 是 S4 的序列面。
//
// 变异「只改 summary 漏 series」会让本用例红 —— 而只跑 TestServiceStatusSummaryReturnsEndpoints
// 的实现在该变异下全绿，这正是 S4 要求两个用例都存在的原因。
func TestServiceStatusSeriesReturnsEndpoints(t *testing.T) {
	sampler := seededEndpointSampler(t, 4)
	body := buildServiceStatusSeriesResponse(sampler)

	if body.Endpoints.Search.TotalRequests != 24 {
		t.Fatalf("序列 search 桶请求数期望 24（4 分钟 × 6），got=%d —— "+
			"为 0 说明 series 端点没接线分桶（S4 只改 summary 的变异）",
			body.Endpoints.Search.TotalRequests)
	}
	if body.Endpoints.Search.SuccessRequests != 20 {
		t.Fatalf("序列 search 桶成功数期望 20，got=%d", body.Endpoints.Search.SuccessRequests)
	}
	if body.Endpoints.Search.Samples != 4 {
		t.Fatalf("序列 search 桶 samples 期望 4，got=%d", body.Endpoints.Search.Samples)
	}
	if body.Endpoints.Other.TotalRequests != 16 {
		t.Fatalf("序列 other 桶请求数期望 16，got=%d", body.Endpoints.Other.TotalRequests)
	}
	if body.Endpoints.Search.AvgLatencyMs != 80 {
		t.Fatalf("序列 search 桶平均延迟期望 80，got=%d", body.Endpoints.Search.AvgLatencyMs)
	}
}

// TestBothEndpointsShareSameEndpointNumbers 钉「两个端点同口径」：
// 同一份采样器上算出来的两个分桶必须完全一致（不许各算一套）。
func TestBothEndpointsShareSameEndpointNumbers(t *testing.T) {
	sampler := seededEndpointSampler(t, 5)
	summary := buildServiceStatusSummaryResponse(sampler)
	series := buildServiceStatusSeriesResponse(sampler)

	if summary.Endpoints.Search.TotalRequests != series.Endpoints.Search.TotalRequests {
		t.Fatalf("两个端点的 search 请求数必须一致：summary=%d series=%d",
			summary.Endpoints.Search.TotalRequests, series.Endpoints.Search.TotalRequests)
	}
	if summary.Endpoints.Search.Samples != series.Endpoints.Search.Samples {
		t.Fatalf("两个端点的 search samples 必须一致：summary=%d series=%d",
			summary.Endpoints.Search.Samples, series.Endpoints.Search.Samples)
	}
	if summary.Endpoints.Search.AvgLatencyMs != series.Endpoints.Search.AvgLatencyMs {
		t.Fatalf("两个端点的 search 平均延迟必须一致：summary=%d series=%d",
			summary.Endpoints.Search.AvgLatencyMs, series.Endpoints.Search.AvgLatencyMs)
	}
	if summary.Endpoints.Other.TotalRequests != series.Endpoints.Other.TotalRequests {
		t.Fatalf("两个端点的 other 请求数必须一致：summary=%d series=%d",
			summary.Endpoints.Other.TotalRequests, series.Endpoints.Other.TotalRequests)
	}
}

// TestServiceStatusEndpointsEmptyWhenNeverSampled 钉「没采样」的形态：
// 空采样器下两个端点的 buckets 都是零值（samples=0），前端据此显灰。
func TestServiceStatusEndpointsEmptyWhenNeverSampled(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)

	summary := buildServiceStatusSummaryResponse(sampler)
	if summary.Endpoints.Search.Samples != 0 || summary.Endpoints.Other.Samples != 0 {
		t.Fatalf("空采样器两个桶的 samples 都必须是 0，got=%+v", summary.Endpoints)
	}
	if summary.Endpoints.Search.TotalRequests != 0 {
		t.Fatalf("空采样器 search 桶请求数必须是 0，got=%d", summary.Endpoints.Search.TotalRequests)
	}

	series := buildServiceStatusSeriesResponse(sampler)
	if series.Endpoints.Search.Samples != 0 {
		t.Fatalf("空采样器序列端点 search samples 也必须是 0，got=%d", series.Endpoints.Search.Samples)
	}

	// 两个端点的 endpoints 都不得序列化成 null（必须是可以读字段的对象）。
	for name, raw := range map[string]any{"summary": summary, "series": series} {
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"endpoints":null`) {
			t.Fatalf("%s 的 endpoints 不得序列化成 null：%s", name, encoded)
		}
		if !strings.Contains(string(encoded), `"endpoints"`) {
			t.Fatalf("%s 必须带 endpoints 字段：%s", name, encoded)
		}
		if !strings.Contains(string(encoded), `"samples"`) {
			t.Fatalf("%s 的分桶必须带 samples（前端判显灰的唯一依据）：%s", name, encoded)
		}
	}
}

// TestPublicEndpointsHideFailedRequests 钉暴露面：
// 两个**公开**端点的分桶都不给 failedRequests。
//
// 与 publicStatusPoint 同一条取舍：失败数可由 total-success 推出，显式给出反而
// 暗示「这里有个指标值得盯」；分桶之后它更敏感（「搜题桶失败 100 次」直接就是
// 「有人打搜题接口且全失败」，而顶层失败数会被 other 稀释）。
func TestPublicEndpointsHideFailedRequests(t *testing.T) {
	sampler := seededEndpointSampler(t, 2)

	seriesRaw, err := json.Marshal(buildServiceStatusSeriesResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	summaryRaw, err := json.Marshal(buildServiceStatusSummaryResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"series": seriesRaw, "summary": summaryRaw} {
		if strings.Contains(string(raw), `"failedRequests"`) {
			t.Fatalf("%s 端点不得包含 failedRequests（包括分桶里）：%s", name, raw)
		}
	}
	// 反向证明种子确实带了非零失败数（否则上面的断言可能是空的）。
	points := sampler.Points()
	if len(points) == 0 || points[0].Endpoints.Search.FailedRequests == 0 {
		t.Fatal("前置失效：种子点的 search 桶必须带非零 FailedRequests，否则剔除断言是空的")
	}
}

// TestPublicEndpointsWhitelisted 用白名单钉公开分桶的字段集合。
//
// 与 publicPointKeys 同一条纪律：白名单能挡住「将来给 EndpointCounters 加字段
// 时顺手泄漏给普通用户」，黑名单挡不住。
func TestPublicEndpointsWhitelisted(t *testing.T) {
	sampler := seededEndpointSampler(t, 2)
	raw, err := json.Marshal(buildServiceStatusSeriesResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	endpoints, ok := top["endpoints"].(map[string]any)
	if !ok {
		t.Fatalf("endpoints 必须是 object，got=%T", top["endpoints"])
	}
	// 桶名白名单：只允许两个固定枚举。
	wantBuckets := []string{"other", "search"}
	gotBuckets := jsonKeysOf(t, endpoints)
	if strings.Join(gotBuckets, ",") != strings.Join(wantBuckets, ",") {
		t.Fatalf("分桶名必须是 %v，实际 %v（出现第三个桶说明按 route 统计回来了）",
			wantBuckets, gotBuckets)
	}
	// 每个桶的字段白名单。
	wantFields := []string{"avgLatencyMs", "samples", "successRequests", "totalRequests"}
	for _, name := range wantBuckets {
		bucket, ok := endpoints[name].(map[string]any)
		if !ok {
			t.Fatalf("桶 %s 必须是 object", name)
		}
		got := jsonKeysOf(t, bucket)
		if strings.Join(got, ",") != strings.Join(wantFields, ",") {
			t.Fatalf("桶 %s 的字段集合必须是 %v，实际 %v\n"+
				"多出的字段意味着「内部 EndpointCounters 的字段被顺手带给了普通用户」",
				name, wantFields, got)
		}
	}
}

// TestServiceStatusSummaryEndpointsHTTPVisible 走真实 gin 上下文确认分桶真的
// 出现在 HTTP 响应体里（防止「只改结构体不改序列化」）。
func TestServiceStatusSummaryEndpointsHTTPVisible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/service-status/summary", nil)

	GetServiceStatusSummary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，got=%d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	endpoints, ok := payload["endpoints"].(map[string]any)
	if !ok {
		t.Fatalf("HTTP 响应缺少 endpoints 分桶：%s", w.Body.String())
	}
	if _, ok := endpoints["search"]; !ok {
		t.Fatalf("HTTP 响应缺少 endpoints.search：%s", w.Body.String())
	}
	// 分层不变：分桶不得带出逐分钟数据。
	for _, forbidden := range []string{`"points"`, `"startMinute"`, `"endMinute"`} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("摘要端点带上分桶后仍不得包含 %s：%s", forbidden, w.Body.String())
		}
	}
}

// TestServiceStatusSeriesEndpointsHTTPVisible 同上，序列端点面。
func TestServiceStatusSeriesEndpointsHTTPVisible(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/service-status/series", nil)

	GetServiceStatusSeries(c)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，got=%d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	endpoints, ok := payload["endpoints"].(map[string]any)
	if !ok {
		t.Fatalf("序列端点的 HTTP 响应缺少 endpoints 分桶（S4 只改 summary 的症状）：%s", w.Body.String())
	}
	if _, ok := endpoints["search"]; !ok {
		t.Fatalf("序列端点缺少 endpoints.search：%s", w.Body.String())
	}
}
