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

// 本文件钉住「摘要端点」与「管理端全量端点」的**分层**。
//
// 分层不是靠前端不渲染实现的 —— 摘要端点根本不能返回能反推时间分布的数据。
// 否则「放宽鉴权给非管理员」这件事只是换了个 URL，信息量一点没少。

// 摘要响应里**不允许**出现的字段名。每一条都对应一种能反推时间分布的能力：
//
//	points     —— 逐分钟的成功/失败数
//	breakdown  —— 按结果分类的切片
//	startMinute / endMinute —— 窗口端点（配合 totalRequests 能反推速率）
//	retained   —— 保留点数（虽然它也在摘要里，但它是整数不是序列，放行）
var forbiddenSummaryKeys = []string{
	`"points"`,
	`"breakdown"`,
	`"startMinute"`,
	`"endMinute"`,
}

// TestServiceStatusSummaryHasNoTimelineData 是这条分层的主判据。
func TestServiceStatusSummaryHasNoTimelineData(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)
	body := buildServiceStatusSummaryResponse(sampler)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range forbiddenSummaryKeys {
		if strings.Contains(text, key) {
			t.Fatalf("摘要响应不得包含 %s（它能把时间分布反推出来）：%s", key, text)
		}
	}
}

// TestServiceStatusSummaryKeepsAggregates 确认摘要没有为了「去明细」把该给的也删了。
//
// 这几项是 PLUS 状态 Tab 唯一需要的东西：现在好不好、多少请求、成功多少。
func TestServiceStatusSummaryKeepsAggregates(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)
	body := buildServiceStatusSummaryResponse(sampler)

	// 空缓冲：窗口内没有请求 ⇒ successRateAvailable 必须是 false。
	//
	// 这一条是本文件最重要的断言：只给 successRate=0 的话，前端无法区分
	// 「全失败」（要报警）与「没请求」（正常夜间），而这两者的用户可见后果完全相反。
	if body.SuccessRateAvailable {
		t.Fatal("空缓冲时 successRateAvailable 必须为 false —— 否则「没有数据」会被显示成「成功率 0%」")
	}
	if body.TotalRequests != 0 {
		t.Fatalf("空缓冲 totalRequests 期望 0，got=%d", body.TotalRequests)
	}
	// 这些是结构性字段，空缓冲也必须有值。
	if body.SchemaVersion != 1 {
		t.Fatalf("schemaVersion 期望 1，got=%d", body.SchemaVersion)
	}
	if body.Capacity != 1440 {
		t.Fatalf("capacity 期望 1440，got=%d", body.Capacity)
	}
	if body.Status != "unknown" {
		t.Fatalf("空缓冲 status 期望 unknown，got=%q", body.Status)
	}
	if body.RestartedAt == "" {
		t.Fatal("restartedAt 不可为空 —— 前端要用它显示「数据自某时刻起」")
	}
}

// TestServiceStatusSummaryDistinguishesAllFailedFromNoTraffic 是本文件的核心对照组。
//
// 只测「空缓冲」是不够的：那里的 successRate 与 totalRequests 同时为 0，用
// `SuccessRate != 0` 和用 `TotalRequests > 0` 判会得到**相同**结果，变异不会红
// （实测踩到：把判据改成 SuccessRate!=0 后全部用例仍绿）。
//
// 真正要挡的场景是「有请求但全失败」：
//
//	窗口内 100 个请求、0 个成功 ⇒ successRate = 0 但这是**最该报警**的状态；
//	若用 successRate!=0 判「有没有数据」，这种状态会被显示成「暂无采样数据」，
//	于是**服务全挂时面板显示一切正常**。这比不做状态页更危险。
func TestServiceStatusSummaryDistinguishesAllFailedFromNoTraffic(t *testing.T) {
	fixed := time.Date(2026, 11, 14, 1, 30, 0, 0, time.UTC)
	ring := service.NewServiceStatusRing()
	sampler := service.NewServiceStatusSampler(ring, func() time.Time { return fixed }, nil)

	// 注入一个「100 请求、0 成功」的分钟点。
	ring.Append(service.StatusPoint{
		Minute:          fixed.Unix() / 60,
		TotalRequests:   100,
		SuccessRequests: 0,
		FailedRequests:  100,
	})

	body := buildServiceStatusSummaryResponse(sampler)

	if body.TotalRequests != 100 {
		t.Fatalf("totalRequests 期望 100，got=%d", body.TotalRequests)
	}
	if body.SuccessRequests != 0 || body.FailedRequests != 100 {
		t.Fatalf("成功/失败期望 0/100，got=%d/%d", body.SuccessRequests, body.FailedRequests)
	}
	// 核心断言：全失败时 successRateAvailable 必须为 **true**。
	//
	// 这一条与空缓冲那条（必须 false）互斥，两条一起才能把
	// 「用 SuccessRate!=0 判可用性」的实现钉死。
	if !body.SuccessRateAvailable {
		t.Fatal("全失败（100 请求 0 成功）时 successRateAvailable 必须为 true。" +
			"为 false 会让「服务全挂」被前端显示成「暂无采样数据」，比没有状态页更危险")
	}
	if body.SuccessRate != 0 {
		t.Fatalf("全失败时 successRate 期望 0，got=%v", body.SuccessRate)
	}
}

// TestGetServiceStatusSummaryHTTPResponse 走真实 gin 上下文，钉状态码与缓存头，
// 并确认响应体里确实没有 points（HTTP 层再验一次，防止有人只改结构体不改序列化）。
func TestGetServiceStatusSummaryHTTPResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/service-status/summary?probe=1", nil)

	GetServiceStatusSummary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，got=%d", w.Code)
	}
	// 与 /admin/service-status 同一条纪律：实时数据不许被任何缓存持有。
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control 期望 no-store，got=%q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if _, exists := payload["points"]; exists {
		t.Fatal("HTTP 响应里出现了 points —— 摘要端点必须没有时间序列")
	}
	if _, exists := payload["successRateAvailable"]; !exists {
		t.Fatal("HTTP 响应缺少 successRateAvailable —— 前端无法区分「全失败」与「没请求」")
	}
	// 查询串不得进响应（与 /system-metrics 同一条纪律）。
	if strings.Contains(w.Body.String(), "probe") {
		t.Fatalf("查询串被带进了响应：%s", w.Body.String())
	}
}
