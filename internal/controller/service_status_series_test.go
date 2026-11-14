package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 本文件钉住「脱敏逐分钟序列端点」的三条边界：
//
//  1. 字段集合是**白名单**，不是黑名单查关键字 —— 将来给 StatusPoint 加字段时，
//     如果忘了同步这里，本测试必须红。黑名单永远会漏掉「新加的那个字段」。
//  2. 失败四分类（尤其 AuthFailures）不得出现：它在爆破/撞库时会尖峰，
//     等于把「攻击正在生效」实时回显给攻击者。
//  3. 空窗口返回 [] 而不是 1440 个零点：补零会画出「全站零流量」的假曲线，
//     与「服务没起来」在视觉上同形。

// 公开序列的**允许**字段集合（白名单）。
//
// 与 admin 端点 serviceStatusResponse 的对照：
//
//	admin   schemaVersion / status / windowMinutes / points / summary / capacity / retained / restartedAt
//	public  schemaVersion / points / capacity / retained / restartedAt
//
// status/windowMinutes/summary 不在公开序列里 —— 那些由 summary 端点提供，
// 序列端点只负责「时间轴」。两个端点各管一段，避免同一份数据两条契约漂移。
var publicSeriesKeys = []string{
	"schemaVersion", "points", "capacity", "retained", "restartedAt",
}

// 单个点的允许字段集合（白名单）。
var publicPointKeys = []string{
	"minute", "totalRequests", "successRequests", "avgLatencyMs",
}

// seededSampler 造一个带 N 个分钟点的采样器，返回采样器与固定时钟基准。
func seededSampler(t *testing.T, minutes int) (*service.ServiceStatusSampler, time.Time) {
	t.Helper()
	fixed := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	ring := service.NewServiceStatusRing()
	sampler := service.NewServiceStatusSampler(ring, func() time.Time { return fixed }, nil)
	for i := 0; i < minutes; i++ {
		ring.Append(service.StatusPoint{
			Minute:          fixed.Unix()/60 + int64(i),
			TotalRequests:   100,
			SuccessRequests: 98,
			FailedRequests:  2,
			// 四个分类都填非零值：这样才能证明它们**真的被剔除**，
			// 而不是「恰好是 0 所以看起来没有」。
			UpstreamFailures: 1,
			TimeoutFailures:  0,
			AuthFailures:     2,
			QuotaFailures:    0,
			AvgLatencyMs:     240,
		})
	}
	return sampler, fixed
}

// jsonKeysOf 返回一个 JSON object 的键集合（排序后）。
func jsonKeysOf(t *testing.T, raw map[string]any) []string {
	t.Helper()
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestPublicStatusSeriesFieldSetIsWhitelisted 是判据 2 的核心：
// 用**白名单**断言字段集合，而不是黑名单查关键字。
//
// 为什么必须白名单：黑名单只能挡住「我已经想到的字段」。将来有人给
// service.StatusPoint 加一个 internalNote 或 upstreamHost，黑名单不会红，
// 而白名单会 —— 因为新字段不在允许集合里。
func TestPublicStatusSeriesFieldSetIsWhitelisted(t *testing.T) {
	sampler, _ := seededSampler(t, 3)
	raw, err := json.Marshal(buildServiceStatusSeriesResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}

	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}

	got := jsonKeysOf(t, top)
	want := append([]string(nil), publicSeriesKeys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("顶层字段集合必须是白名单 %v，实际 %v\n"+
			"多出的字段意味着「新增的 StatusPoint/admin 字段被顺手带给了普通用户」", want, got)
	}

	// 逐个点同样用白名单。
	points, ok := top["points"].([]any)
	if !ok || len(points) != 3 {
		t.Fatalf("points 期望 3 个，got=%v", top["points"])
	}
	wantPoint := append([]string(nil), publicPointKeys...)
	sort.Strings(wantPoint)
	for i, item := range points {
		point, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("第 %d 个点不是 object", i)
		}
		gotPoint := jsonKeysOf(t, point)
		if strings.Join(gotPoint, ",") != strings.Join(wantPoint, ",") {
			t.Fatalf("第 %d 个点的字段集合必须是 %v，实际 %v", i, wantPoint, gotPoint)
		}
	}
}

// TestPublicStatusSeriesExcludesFailureBreakdown 是判据 3：
// 失败四分类**一律**不出现在公开序列里。
//
// 种子点的四个分类故意造了非零值（AuthFailures=2），所以「剔除」是真的剔除，
// 不是「值恰好是 0 所以搜不到」。
func TestPublicStatusSeriesExcludesFailureBreakdown(t *testing.T) {
	sampler, _ := seededSampler(t, 5)
	raw, err := json.Marshal(buildServiceStatusSeriesResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	for _, key := range []string{
		`"failedRequests"`, `"upstreamFailures"`, `"timeoutFailures"`,
		`"authFailures"`, `"quotaFailures"`, `"breakdown"`,
	} {
		if strings.Contains(text, key) {
			t.Fatalf("公开序列不得包含 %s —— 失败分类是运维定位用，且 authFailures 会在爆破时尖峰：%s", key, text)
		}
	}

	// 反向证明种子确实带了这些值（否则上面的断言可能因为「数据本来就没有」而假绿）。
	ringPoints := sampler.Points()
	if len(ringPoints) == 0 || ringPoints[0].AuthFailures == 0 {
		t.Fatalf("前置失效：种子点必须带非零 AuthFailures（当前 %d），否则剔除断言是空的", ringPoints[0].AuthFailures)
	}
}

// TestPublicStatusSeriesKeepsChartableFields 确认「脱敏」没有脱过头：
// 画曲线需要的三个量必须真的在。
func TestPublicStatusSeriesKeepsChartableFields(t *testing.T) {
	sampler, fixed := seededSampler(t, 4)
	body := buildServiceStatusSeriesResponse(sampler)

	if body.SchemaVersion != 1 {
		t.Fatalf("schemaVersion 期望 1，got=%d", body.SchemaVersion)
	}
	if body.Capacity != 1440 {
		t.Fatalf("capacity 期望 1440，got=%d", body.Capacity)
	}
	if body.Retained != 4 || len(body.Points) != 4 {
		t.Fatalf("retained/points 期望 4，got=%d/%d", body.Retained, len(body.Points))
	}
	if body.RestartedAt == "" {
		t.Fatal("restartedAt 不可为空 —— 前端要用它标注曲线起点")
	}

	first := body.Points[0]
	if first.Minute != fixed.Unix()/60 {
		t.Fatalf("minute 期望 %d，got=%d", fixed.Unix()/60, first.Minute)
	}
	if first.TotalRequests != 100 || first.SuccessRequests != 98 || first.AvgLatencyMs != 240 {
		t.Fatalf("可画图字段值错：%+v", first)
	}
	// 顺序必须与环形缓冲一致（升序），否则曲线会乱。
	for i := 1; i < len(body.Points); i++ {
		if body.Points[i].Minute != body.Points[i-1].Minute+1 {
			t.Fatalf("点必须按分钟升序，位置 %d：%d 之后是 %d",
				i, body.Points[i-1].Minute, body.Points[i].Minute)
		}
	}
}

// TestPublicStatusSeriesEmptyWindowIsEmptyArray 是判据 4：
// 空窗口 → points 是 []，不补 0 桶。
//
// 补零的后果不是「多几条数据」，而是画出一条「全站零流量」的假曲线 ——
// 它与「服务完全没起来」在视觉上同形，比不显示更误导。
func TestPublicStatusSeriesEmptyWindowIsEmptyArray(t *testing.T) {
	sampler := service.NewServiceStatusSampler(service.NewServiceStatusRing(), nil, nil)
	body := buildServiceStatusSeriesResponse(sampler)

	if body.Points == nil {
		t.Fatal("空窗口必须返回非 nil 的空切片（序列化成 [] 而不是 null）")
	}
	if len(body.Points) != 0 {
		t.Fatalf("空窗口必须返回 0 个点，got=%d（补 1440 个零点会画出假曲线）", len(body.Points))
	}
	if body.Retained != 0 {
		t.Fatalf("空窗口 retained 期望 0，got=%d", body.Retained)
	}
	// capacity 仍然是真实容量 —— 它是「上限」不是「现有数量」。
	if body.Capacity != 1440 {
		t.Fatalf("capacity 期望 1440（上限不随数据变化），got=%d", body.Capacity)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"points":[]`) {
		t.Fatalf("空窗口必须序列化成 \"points\":[]，got=%s", raw)
	}
	if strings.Contains(string(raw), `"points":null`) {
		t.Fatalf("空窗口不得序列化成 null：%s", raw)
	}
}

// TestGetServiceStatusSeriesHTTPContract 走真实 gin 上下文，钉状态码、缓存头、
// 以及「查询串不进响应」（与 summary / system-metrics 同一条纪律）。
func TestGetServiceStatusSeriesHTTPContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/service-status/series?range=24h&probe=PRIVATE_SERIES_SENTINEL", nil)

	GetServiceStatusSeries(c)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码期望 200，got=%d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control 期望 no-store（实时数据不许被缓存持有），got=%q", got)
	}
	if strings.Contains(w.Body.String(), "PRIVATE_SERIES_SENTINEL") {
		t.Fatalf("查询串被带进了响应：%s", w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if _, ok := payload["points"]; !ok {
		t.Fatal("响应缺少 points —— 这是序列端点的唯一职责")
	}
}

// TestPublicStatusSeriesIsNotAdminResponse 确认它**没有**复用 admin 的响应结构。
//
// 这条挡的是「图省事直接返回 serviceStatusResponse」的实现：那样会把
// summary（含 breakdown 级的推导空间）和全字段 points 一起给普通用户，
// 而且将来 admin 加字段会自动泄漏。
func TestPublicStatusSeriesIsNotAdminResponse(t *testing.T) {
	sampler, _ := seededSampler(t, 2)
	raw, err := json.Marshal(buildServiceStatusSeriesResponse(sampler))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	// admin 端点独有的顶层字段，公开序列一个都不能有。
	for _, key := range []string{`"summary"`, `"status"`, `"windowMinutes"`} {
		if strings.Contains(text, key) {
			t.Fatalf("公开序列不得包含 admin 端点字段 %s（应独立定义，避免将来 admin 加字段自动泄漏）：%s", key, text)
		}
	}
}
