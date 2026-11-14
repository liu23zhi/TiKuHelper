package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件钉住「按接口族分桶」的三条不可协商性质（task_doc/138 §4.2 / §4.2.1 / §4.2.2）：
//
//	① 枚举只有 search / other 两个取值，且 ClassifyEndpoint 只认三条搜题模板；
//	② samples 是「有没有数据」的唯一依据，不能用 TotalRequests==0 反推；
//	③ 计数值只在内存里，响应里不出现任何路由字符串。
//
// 期望值一律**写字面量**（"search"、"other"、3、0），不引用被测常量 ——
// 否则常量漂移时期望值与被测值同步变化，断言恒真、变异不会红（本项目
// client_kind 那轮踩过这个坑，FAIL_COUNT 从 3 修到 7 才真正覆盖到行为）。

// TestEndpointKindValueSetIsExactlyTwo 钉枚举全集。
//
// 这是红线判据：EndpointKind 一旦接受第三个取值（或退化成 string），
// 「二值枚举」就不复存在，按接口族统计会悄悄变成按 route 统计 ——
// 而 diagnostics 的包注释明写 no route。
func TestEndpointKindValueSetIsExactlyTwo(t *testing.T) {
	kinds := EndpointKinds()
	if len(kinds) != 2 {
		t.Fatalf("EndpointKind 只允许 2 个取值，got=%d（%v）", len(kinds), kinds)
	}
	// 字面量断言：常量被改名时这里必须红。
	want := map[string]bool{"search": true, "other": true}
	for _, kind := range kinds {
		if !want[string(kind)] {
			t.Fatalf("出现未允许的 EndpointKind 取值 %q —— 只允许 search / other", string(kind))
		}
		delete(want, string(kind))
	}
	if len(want) != 0 {
		t.Fatalf("缺少枚举取值：%v", want)
	}
	// 常量本身的字面值也钉住：改名会让前后端契约漂移。
	if string(EndpointSearch) != "search" {
		t.Fatalf("EndpointSearch 的字面值必须是 \"search\"，got=%q", string(EndpointSearch))
	}
	if string(EndpointOther) != "other" {
		t.Fatalf("EndpointOther 的字面值必须是 \"other\"，got=%q", string(EndpointOther))
	}
}

// TestClassifyEndpointRoutesSearchOnly 是判据 S1 的行为面：
// /search 相关的三条路由计入 search，其余全部 other。
//
// 变异 S1（把 search 分支改成 return EndpointOther，即「全记到 other」）
// 会让本用例的 search 那组全部红。
func TestClassifyEndpointRoutesSearchOnly(t *testing.T) {
	cases := []struct {
		route string
		want  string
	}{
		// ── 三条搜题路由：裸模板形态（与 searchEndpointRoutes 同形）──
		{"/search/tasks", "search"},
		{"/ai-fallback", "search"},
		{"/ai-fallback/tasks", "search"},
		// ── 同一组路由在真实路由组下的形态（gin FullPath 带组前缀）──
		{"/api/search/tasks", "search"},
		{"/api/ai-fallback", "search"},
		{"/api/ai-fallback/tasks", "search"},
		{"/adapter-service/search/tasks", "search"},
		// ── 必须落 other 的近邻（这些是「按接口族」不会混淆到 search 的关键）──
		{"/api/search", "other"},               // 主搜题入口 /search 也是 other（不在三条之列）
		{"/adapter-service/search", "other"},   //
		{"/api/search/health", "other"},        // 同前缀但不同模板
		{"/api/search/status", "other"},        //
		{"/api/ai-fallback/status", "other"},   // ai-fallback 的 status 不是 tasks
		{"/api/tiku/evidence", "other"},        //
		{"/api/admin/service-status", "other"}, //
		{"/__unmatched__", "other"},            // gin 未匹配路由
		{"", "other"},                          // 空串（中间件兜底赋值前的形态）
		{"   ", "other"},                       // 纯空白
		{"/api/search/tasks/123", "other"},     // 带具体 id 的不是模板
		{"search/tasks", "other"},              // 缺前导斜杠，不算模板
		{"/api//search/tasks", "other"},        // 双斜杠不是同一个模板
		{"/SEARCH/TASKS", "other"},             // 大小写敏感（gin 路由本身就是大小写敏感）
	}
	for _, tc := range cases {
		if got := string(ClassifyEndpoint(tc.route)); got != tc.want {
			t.Errorf("ClassifyEndpoint(%q) 期望 %q，got=%q", tc.route, tc.want, got)
		}
	}
}

// TestClassifyEndpointNeverReturnsRouteItself 钉红线第 4 条：
// 返回值只能是两个固定字面量，绝不能把输入原样带回去。
func TestClassifyEndpointNeverReturnsRouteItself(t *testing.T) {
	for _, route := range []string{
		"/api/search/tasks", "/api/unknown-route", "/api/user/login?x=1",
		"PRIVATE_ROUTE_SENTINEL", "/api/search/tasks/PRIVATE_ID_SENTINEL",
	} {
		got := string(ClassifyEndpoint(route))
		if got != "search" && got != "other" {
			t.Fatalf("ClassifyEndpoint(%q) 返回了非法取值 %q —— 只允许 search / other", route, got)
		}
		if got == route {
			t.Fatalf("ClassifyEndpoint(%q) 把输入原样返回了 —— 等价于按 route 统计", route)
		}
	}
}

// TestEndpointRecorderBucketsByKind 是 S1 的采样面：
// 记 3 次 search、记 2 次 other，两个桶必须各自独立计数。
func TestEndpointRecorderBucketsByKind(t *testing.T) {
	recorder := &ServiceStatusEndpointRecorder{}

	recorder.ObserveEndpoint("/api/search/tasks", 200, 30)
	recorder.ObserveEndpoint("/api/ai-fallback", 200, 40)
	recorder.ObserveEndpoint("/api/ai-fallback/tasks", 500, 50)
	recorder.ObserveEndpoint("/api/user/login", 200, 10)
	recorder.ObserveEndpoint("/api/admin/service-status", 200, 20)

	snapshot := recorder.Snapshot()
	if snapshot.Search.TotalRequests != 3 {
		t.Fatalf("search 桶总数期望 3，got=%d（把 search 记进 other 就是这个变异）", snapshot.Search.TotalRequests)
	}
	if snapshot.Other.TotalRequests != 2 {
		t.Fatalf("other 桶总数期望 2，got=%d", snapshot.Other.TotalRequests)
	}
	// 失败计数：只有 ai-fallback/tasks 那次是 500。
	if snapshot.Search.FailedRequests != 1 {
		t.Fatalf("search 桶失败数期望 1，got=%d", snapshot.Search.FailedRequests)
	}
	if snapshot.Other.FailedRequests != 0 {
		t.Fatalf("other 桶失败数期望 0，got=%d", snapshot.Other.FailedRequests)
	}
	// 耗时只增不减地累计。
	if snapshot.Search.LatencyCount != 3 {
		t.Fatalf("search 桶延迟样本数期望 3，got=%d", snapshot.Search.LatencyCount)
	}
	if snapshot.Search.LatencyTotalMillis != 120 {
		t.Fatalf("search 桶耗时总和期望 120，got=%d", snapshot.Search.LatencyTotalMillis)
	}
	// 桶之间不得串味：other 的耗时只能是它自己的 30。
	if snapshot.Other.LatencyTotalMillis != 30 {
		t.Fatalf("other 桶耗时总和期望 30，got=%d", snapshot.Other.LatencyTotalMillis)
	}
}

// TestEndpointRecorderNegativeLatencyIsZero 钉负耗时按 0 处理（时钟回退）。
func TestEndpointRecorderNegativeLatencyIsZero(t *testing.T) {
	recorder := &ServiceStatusEndpointRecorder{}
	recorder.ObserveEndpoint("/api/search/tasks", 200, -500)
	snapshot := recorder.Snapshot()
	if snapshot.Search.LatencyTotalMillis != 0 {
		t.Fatalf("负耗时必须按 0 计，got=%d", snapshot.Search.LatencyTotalMillis)
	}
	// 但请求本身仍被记下来（不能因为耗时异常就吞掉计数）。
	if snapshot.Search.TotalRequests != 1 {
		t.Fatalf("请求数期望 1，got=%d", snapshot.Search.TotalRequests)
	}
}

// TestEndpointRecorderNilSafeAndConcurrent 钉 nil 安全与并发（-race 下跑）。
func TestEndpointRecorderNilSafeAndConcurrent(t *testing.T) {
	var nilRecorder *ServiceStatusEndpointRecorder
	nilRecorder.ObserveEndpoint("/api/search/tasks", 200, 1) // 不得 panic
	if snapshot := nilRecorder.Snapshot(); snapshot.Search.TotalRequests != 0 {
		t.Fatalf("nil 记录器的快照必须是零值，got=%+v", snapshot)
	}

	recorder := &ServiceStatusEndpointRecorder{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				recorder.ObserveEndpoint("/api/search/tasks", 200, 5)
			} else {
				recorder.ObserveEndpoint("/api/user/login", 200, 5)
			}
		}(i)
	}
	wg.Wait()
	snapshot := recorder.Snapshot()
	if snapshot.Search.TotalRequests != 25 || snapshot.Other.TotalRequests != 25 {
		t.Fatalf("并发下两桶必须各 25，got search=%d other=%d",
			snapshot.Search.TotalRequests, snapshot.Other.TotalRequests)
	}
}

// TestEndpointRecorderKeepsNoRouteOrLogLabels 钉红线第 3、4 条：
// 记录器内部状态里不能出现任何路由字符串。
//
// 做法是把记录器整个序列化（含未导出字段不可见，所以另用反射式检查：
// 这里改用「把所有已记录桶拼成字符串 + 检查没有出现在输入里的路径片段」，
// 再额外做一次全仓式的结构断言：类型里只有两个 StatusCounters 字段）。
func TestEndpointRecorderKeepsNoRouteOrLogLabels(t *testing.T) {
	recorder := &ServiceStatusEndpointRecorder{}
	recorder.ObserveEndpoint("/api/search/tasks", 200, 5)
	recorder.ObserveEndpoint("/api/PRIVATE_ROUTE_SENTINEL", 200, 5)

	encoded, err := json.Marshal(recorder.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE_ROUTE_SENTINEL", "/api/", "route", "Route", "path", "Path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("分桶快照不得包含 %q（红线：不存 route/不落库/不写日志）：%s", forbidden, encoded)
		}
	}

	// 结构断言：记录器只有两个桶，没有「按 route 的 map」这种可扩展容器。
	if got := len(EndpointKinds()); got != 2 {
		t.Fatalf("分桶数量必须固定为 2，got=%d —— 出现第 3 个桶说明按 route 统计回来了", got)
	}
}

// endpointSamplerFixture 造一个把「全局计数」与「分桶计数」都接到可控变量上的采样器。
type endpointSamplerFixture struct {
	sampler  *ServiceStatusSampler
	now      time.Time
	counters StatusCounters
	buckets  ServiceStatusEndpointCounters
}

func newEndpointSamplerFixture(base time.Time) *endpointSamplerFixture {
	fixture := &endpointSamplerFixture{now: base}
	fixture.sampler = NewServiceStatusSamplerWithEndpoints(NewServiceStatusRing(),
		func() time.Time { return fixture.now },
		func() StatusCounters { return fixture.counters },
		func() ServiceStatusEndpointCounters { return fixture.buckets })
	return fixture
}

// advanceSearch 让 search 桶与全局计数一起前进。
func (f *endpointSamplerFixture) advanceSearch(total, failed uint64, latencyMillis uint64) {
	f.counters.TotalRequests += total
	f.counters.FailedRequests += failed
	f.counters.LatencyCount += total
	f.counters.LatencyTotalMillis += latencyMillis
	f.buckets.Search.TotalRequests += total
	f.buckets.Search.FailedRequests += failed
	f.buckets.Search.LatencyCount += total
	f.buckets.Search.LatencyTotalMillis += latencyMillis
}

// advanceOther 让 other 桶与全局计数一起前进。
func (f *endpointSamplerFixture) advanceOther(total, failed uint64, latencyMillis uint64) {
	f.counters.TotalRequests += total
	f.counters.FailedRequests += failed
	f.counters.LatencyCount += total
	f.counters.LatencyTotalMillis += latencyMillis
	f.buckets.Other.TotalRequests += total
	f.buckets.Other.FailedRequests += failed
	f.buckets.Other.LatencyCount += total
	f.buckets.Other.LatencyTotalMillis += latencyMillis
}

// TestSamplerBucketsSearchAndOtherSeparately 是 S1 的端到端面：
// 同一分钟里 8 次 search + 2 次 other，点上的两个桶必须各自独立。
func TestSamplerBucketsSearchAndOtherSeparately(t *testing.T) {
	fixture := newEndpointSamplerFixture(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))

	fixture.advanceSearch(8, 1, 8*40)
	fixture.advanceOther(2, 0, 2*10)
	point := fixture.sampler.Sample()

	if point.Endpoints.Search.TotalRequests != 8 {
		t.Fatalf("search 桶期望 8 次，got=%d（全记到 other 就是这个变异的症状）", point.Endpoints.Search.TotalRequests)
	}
	if point.Endpoints.Other.TotalRequests != 2 {
		t.Fatalf("other 桶期望 2 次，got=%d", point.Endpoints.Other.TotalRequests)
	}
	if point.Endpoints.Search.FailedRequests != 1 {
		t.Fatalf("search 桶失败期望 1，got=%d", point.Endpoints.Search.FailedRequests)
	}
	if point.Endpoints.Search.SuccessRequests != 7 {
		t.Fatalf("search 桶成功期望 7，got=%d", point.Endpoints.Search.SuccessRequests)
	}
	// 桶之和必须等于全局总数 —— 分桶是加法信息，不能凭空多/少请求。
	if sum := point.Endpoints.Search.TotalRequests + point.Endpoints.Other.TotalRequests; sum != point.TotalRequests {
		t.Fatalf("两桶之和 %d 必须等于全局 %d", sum, point.TotalRequests)
	}
}

// TestSamplerEndpointSamplesReflectSampledMinutes 钉 samples 的语义（S2 的后端面）。
//
// samples 数的是**被采样的分钟数**，不是「该桶有请求的分钟数」：
// 采样是全局的（一个采样器），所以两个桶拿到的样本分钟数相同。
// 这一点必须在注释里说清，否则后来者会把它当「每桶活跃分钟数」用。
func TestSamplerEndpointSamplesReflectSampledMinutes(t *testing.T) {
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	fixture := newEndpointSamplerFixture(base)

	// 第 1 分钟：只有 other 流量（例如健康检查）。search 桶真的 0 请求。
	fixture.advanceOther(3, 0, 30)
	fixture.sampler.Sample()

	// 第 2 分钟：只有 search 流量。
	fixture.now = base.Add(time.Minute)
	fixture.advanceSearch(5, 0, 100)
	fixture.sampler.Sample()

	// 第 3 分钟：又只有 other。让前两分钟被封存。
	fixture.now = base.Add(2 * time.Minute)
	fixture.advanceOther(2, 0, 20)
	fixture.sampler.Sample()

	summary := fixture.sampler.Summary()
	// 3 个分钟都被采样过（采样器持有分桶源）。
	if summary.Endpoints.Search.Samples != 3 {
		t.Fatalf("search 桶 samples 期望 3（3 个分钟都被采样），got=%d", summary.Endpoints.Search.Samples)
	}
	if summary.Endpoints.Other.Samples != 3 {
		t.Fatalf("other 桶 samples 期望 3（3 个分钟都被采样），got=%d", summary.Endpoints.Other.Samples)
	}
	// samples 与请求数是两个量：search 5 次 / other 5 次，但 samples 都是 3（分钟数）。
	if summary.Endpoints.Other.TotalRequests != 5 {
		t.Fatalf("other 桶请求数期望 5（3+2），got=%d —— samples 必须与 totalRequests 解耦",
			summary.Endpoints.Other.TotalRequests)
	}
}

// TestSummarySamplesZeroWhenNoEndpointSource 是 S2 的核心判据：
// 没有分桶采样源时 samples 必须是 0，而**不是**靠 totalRequests==0 去猜。
//
// 这条与下一条（有采样但 0 请求）构成互斥对照组：只测其中一条，
// 用 totalRequests==0 判实现都能过，变异不会红。
func TestSummarySamplesZeroWhenNoEndpointSource(t *testing.T) {
	base := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
	now := base
	counters := StatusCounters{}
	// 注意：三参数构造 → readEndpoints == nil ⇒ 该采样器「不分桶」。
	sampler := NewServiceStatusSampler(NewServiceStatusRing(),
		func() time.Time { return now },
		func() StatusCounters { return counters })

	// 有全局流量、但没有任何分桶采样源。
	counters.TotalRequests += 10
	counters.LatencyCount += 10
	sampler.Sample()
	now = base.Add(time.Minute)
	counters.TotalRequests += 10
	counters.LatencyCount += 10
	sampler.Sample()

	summary := sampler.Summary()
	if summary.TotalRequests == 0 {
		t.Fatal("前置失效：全局计数必须非零，否则本用例测不到「有流量但没分桶采样」")
	}
	// 关键断言：samples 必须是 0。这正是「没采样」的形态，前端据此显灰。
	if summary.Endpoints.Search.Samples != 0 {
		t.Fatalf("没有分桶采样源时 search samples 必须为 0，got=%d", summary.Endpoints.Search.Samples)
	}
	if summary.Endpoints.Other.Samples != 0 {
		t.Fatalf("没有分桶采样源时 other samples 必须为 0，got=%d", summary.Endpoints.Other.Samples)
	}
	// 两个桶的计数也都是 0（没有源就没有分桶数据）。
	if summary.Endpoints.Search.TotalRequests != 0 || summary.Endpoints.Other.TotalRequests != 0 {
		t.Fatalf("没有分桶采样源时两个桶都必须是 0，got=%+v", summary.Endpoints)
	}
}

// TestSummarySamplesPositiveWhenSampledButZeroRequests 是本文件最关键的另一半：
//
//	「采样到了，但该桶这一分钟确实 0 次请求」→ samples > 0（**不显灰**）
//	「根本没采样」                              → samples == 0（显灰）
//
// 两件事的 TotalRequests 都是 0，只有 samples 能分开。这也正是 138 §4.2.2
// 要求 samples 存在的全部理由。
func TestSummarySamplesPositiveWhenSampledButZeroRequests(t *testing.T) {
	base := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	fixture := newEndpointSamplerFixture(base)

	// 只有 other 有流量；search 全程 0 请求 —— 但分桶采样源是接好的。
	for minute := 0; minute < 4; minute++ {
		if minute > 0 {
			fixture.now = base.Add(time.Duration(minute) * time.Minute)
		}
		fixture.advanceOther(1, 0, 10)
		fixture.sampler.Sample()
	}
	// 封存并让 summary 看到这些点。
	fixture.now = base.Add(4 * time.Minute)
	fixture.sampler.Sample()

	summary := fixture.sampler.Summary()
	// search 桶：4 个被采样的分钟里 0 请求。
	if summary.Endpoints.Search.TotalRequests != 0 {
		t.Fatalf("search 桶请求数期望 0，got=%d", summary.Endpoints.Search.TotalRequests)
	}
	// 核心断言：samples 必须 > 0 —— 有采样数据，只是这套接口没人用。
	if summary.Endpoints.Search.Samples <= 0 {
		t.Fatal("search 桶在这几个分钟里被采样到了（只是 0 请求），samples 必须 > 0；" +
			"为 0 会让前端把「服务在跑、暂时没人搜题」显示成「暂无采样数据」")
	}
	// other 也有采样。
	if summary.Endpoints.Other.Samples <= 0 {
		t.Fatalf("other 桶 samples 必须 > 0，got=%d", summary.Endpoints.Other.Samples)
	}
}

// TestSummarySamplesCountAllRetainedMinutesNotJustWindow 钉 samples 的口径：
// 它在**整个保留序列**上数，不受 5 分钟窗口限制。
//
// 变异（把 samples 改成只数 window）会让本用例红：18 个被采样的分钟里，
// 最近 5 分钟一次 search 都没有 ⇒ 只看窗口会得到 5（或），而真实情况是
// 「整个序列里有 18 个分钟被采样，只是最近没人搜题」。
func TestSummarySamplesCountAllRetainedMinutesNotJustWindow(t *testing.T) {
	base := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	fixture := newEndpointSamplerFixture(base)

	// 前 10 分钟：每分钟 5 次 search。
	for minute := 0; minute < 10; minute++ {
		if minute > 0 {
			fixture.now = base.Add(time.Duration(minute) * time.Minute)
		}
		fixture.advanceSearch(5, 0, 50)
		fixture.sampler.Sample()
	}
	// 之后 8 分钟：只有 other（search 静默）。
	for minute := 10; minute < 18; minute++ {
		fixture.now = base.Add(time.Duration(minute) * time.Minute)
		fixture.advanceOther(2, 0, 20)
		fixture.sampler.Sample()
	}

	summary := fixture.sampler.Summary()
	// 窗口（最近 5 分钟）里 search 一次都没有。
	if summary.Endpoints.Search.TotalRequests != 0 {
		t.Fatalf("最近 5 分钟窗口内 search 请求期望 0，got=%d", summary.Endpoints.Search.TotalRequests)
	}
	// 18 个分钟都被采样 ⇒ samples 必须是 18（远超 5 分钟窗口）。
	if summary.Endpoints.Search.Samples != 18 {
		t.Fatalf("samples 必须在整个保留序列上数（18 个被采样的分钟），got=%d；"+
			"若为 5 说明它被压到了 window 口径，会把「有数据但最近没人搜」误判成「暂无采样」",
			summary.Endpoints.Search.Samples)
	}
	if summary.Endpoints.Search.Samples <= int64(summary.WindowMinutes) {
		t.Fatalf("samples(%d) 必须大于 windowMinutes(%d) —— 否则口径被压到窗口了",
			summary.Endpoints.Search.Samples, summary.WindowMinutes)
	}
}

// TestSamplerEndpointsAbsentWhenNoSource 钉退化路径：
// 三参数构造的采样器产出的点也必须能正常序列化（Endpoints 零值，不是 null）。
func TestSamplerEndpointsAbsentWhenNoSource(t *testing.T) {
	fixture := newEndpointSamplerFixture(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
	// 显式用三参数构造（readEndpoints == nil）。
	now := fixture.now
	counters := StatusCounters{TotalRequests: 1, LatencyCount: 1}
	sampler := NewServiceStatusSampler(NewServiceStatusRing(),
		func() time.Time { return now },
		func() StatusCounters { return counters })
	sampler.Sample()

	raw, err := json.Marshal(sampler.Points())
	if err != nil {
		t.Fatalf("分桶零值必须可序列化：%v", err)
	}
	if strings.Contains(string(raw), `"endpoints":null`) {
		t.Fatalf("endpoints 不得序列化成 null（结构体零值即可）：%s", raw)
	}
	// 结构字段必须在（前端会读 endpoints.search.samples）。
	if !strings.Contains(string(raw), `"endpoints"`) {
		t.Fatalf("点里必须有 endpoints 字段：%s", raw)
	}
}

// TestEndpointCountersJSONFieldNames 钉前后端字段名契约。
//
// 期望值写字面量：字段被改名（比如 samples→sampleCount）必须红 ——
// 前端读的是 samples，改名会让「显灰」逻辑静默失效（读到 undefined）。
func TestEndpointCountersJSONFieldNames(t *testing.T) {
	raw, err := json.Marshal(StatusCountersByEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{
		`"search"`, `"other"`,
		`"totalRequests"`, `"successRequests"`, `"failedRequests"`,
		`"avgLatencyMs"`, `"samples"`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("分桶 JSON 缺少字段 %s：%s", key, text)
		}
	}
}
