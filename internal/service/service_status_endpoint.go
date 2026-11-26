package service

import (
	"strings"
	"sync"
	"time"
)

// 本文件只放「按接口族分桶」的固定枚举与内存计数器（task_doc/138 §4.2 / §4.2.1）。
//
// # 为什么必须是固定枚举，而不是把 route 原样记下来
//
// internal/diagnostics/collector.go:2 的包注释是这条设计的红线：
//
//	It stores no user, request, route, question, token, or monetary data.
//
// 「按 route 统计」直接违反它 —— route 本身就是被该注释点名禁止的数据。
// 但「按接口族」只是二值枚举，无法反推出任何具体请求的路径，所以它是这条
// 红线内唯一允许的形态。四条子约束：
//
//  1. EndpointKind 只有 search / other 两个取值（见 EndpointKinds）。
//  2. ClassifyEndpoint 只做固定 switch 映射：空串、未匹配路由（__unmatched__）、
//     任何未知字符串一律落 other，**不做正则匹配、不新增取值**。
//  3. 计数值只活在内存里：不写数据库、不写日志、不进 diagnostics 快照。
//  4. 计数器**不保存 route 字符串本身** —— ClassifyEndpoint 的返回值是唯一
//     被保留下来的信息，原文即弃。
//
// 第 3、4 条一旦破，这里的「二值枚举」就退化成「按接口的持久化统计」，
// 与 diagnostics 的红线直接冲突。变异验证 S1（把 search 记进 other 桶）
// 钉的是分类正确性；枚举取值集合由 TestEndpointKindValueSetIsExactlyTwo 钉住。

// EndpointKind 是接口族的**固定枚举**。只允许下面两个常量，不接受任意字符串。
type EndpointKind string

const (
	// EndpointSearch 覆盖搜题链路的三条路由模板。
	EndpointSearch EndpointKind = "search"
	// EndpointOther 是其余一切的兜底，含空串与 gin 的未匹配路由。
	EndpointOther EndpointKind = "other"
)

// EndpointKinds 返回固定的两个枚举取值（顺序稳定，便于遍历与断言）。
//
// 它的存在本身就是约束：调用方遍历它，而不是反射枚举或接收外部字符串。
func EndpointKinds() []EndpointKind {
	return []EndpointKind{EndpointSearch, EndpointOther}
}

// searchEndpointRoutes 是**唯一**允许落 search 的路由模板，且是**不带组前缀**的形式。
//
// 为什么必须考虑组前缀：gin 的 c.FullPath() 返回的是**含组前缀**的模板。本仓的
// 搜题路由挂在两个组上：
//
//	apiRouter（组前缀 /api）                     → /api/search/tasks、/api/ai-fallback …
//	legacySearchRouter（组前缀 /adapter-service） → /adapter-service/search/tasks
//
// 只按裸模板比较的话线上一个都匹配不上，全部落 other —— 那会让整个功能静默失效
// 而所有用例照绿（「接线层假绿」的典型形态，见 controller 包的
// TestGinFullPathIncludesGroupPrefix）。
var searchEndpointRoutes = [...]string{
	"/search/tasks",
	"/ai-fallback",
	"/ai-fallback/tasks",
}

// endpointRouteGroupPrefixes 是允许被剥掉的路由组前缀（固定白名单）。
//
// 只剥这几个固定串，不做通用前缀裁剪：通用裁剪会让「任何以 /api 开头的东西」
// 参与比较，等价于放宽了 ClassifyEndpoint 的输入面。
var endpointRouteGroupPrefixes = [...]string{
	"/api",
	"/adapter-service",
}

// ClassifyEndpoint 把路由模板映射到接口族。
//
// 只有 /search/tasks、/ai-fallback、/ai-fallback/tasks 三条落 EndpointSearch，
// 其余一律 EndpointOther。入参允许带 apiRouter / legacySearchRouter 的组前缀。
//
// 刻意不接收 http.Request、不读查询串、不读 method：接口族只由「路由模板」决定，
// 这样它既不能反推单次请求，也不依赖任何请求级数据（红线第 2 条）。
func ClassifyEndpoint(route string) EndpointKind {
	trimmed := strings.TrimSpace(route)
	if trimmed == "" {
		return EndpointOther
	}
	for _, prefix := range endpointRouteGroupPrefixes {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	for _, candidate := range searchEndpointRoutes {
		if trimmed == candidate {
			return EndpointSearch
		}
	}
	return EndpointOther
}

// EndpointCounters 是**单个接口族**在一个分钟点（或一个窗口）里的聚合值。
type EndpointCounters struct {
	TotalRequests   int64 `json:"totalRequests"`
	SuccessRequests int64 `json:"successRequests"`
	FailedRequests  int64 `json:"failedRequests"`

	// AvgLatencyMs 是该桶内的平均耗时；请求数为 0 时保持 0。
	AvgLatencyMs int64 `json:"avgLatencyMs"`

	latency statusLatencyTally

	// Samples 是该桶「被采样到」的分钟数 —— **判有没有数据的唯一依据**。
	//
	// 不能用 TotalRequests == 0 判断，因为两件事在数值上完全同形：
	//
	//	samples > 0, totalRequests == 0  → 采样到了，该接口族在这些分钟里确实 0 次请求
	//	samples == 0, totalRequests == 0  → 根本没采样（服务刚重启/采样未接线）
	//
	// 前者不该显灰，后者必须显灰。见 138 §4.2.2，与「lazy-create 产物缺失
	// 不能推断生产者没运行」是同一条判据。
	Samples int64 `json:"samples"`
}

// StatusCountersByEndpoint 是按接口族分桶的聚合值。字段固定为两个，不可扩展。
type StatusCountersByEndpoint struct {
	Search EndpointCounters `json:"search"`
	Other  EndpointCounters `json:"other"`
}

// ServiceStatusEndpointCounters 是分桶**单调累计**计数快照（采样器做差的输入）。
type ServiceStatusEndpointCounters struct {
	Search StatusCounters
	Other  StatusCounters
}

// ServiceStatusEndpointRecorder 记录按接口族分桶的进程内单调累计计数。
//
// 与 StatusFailureRecorder 同构：采样器相邻两次采样做差，得到「这一分钟发生了什么」；
// 直接放累计值进去会把曲线画成永远上升的总量。
//
// 只增不减、定长内存（两个 StatusCounters）、无动态标签 —— 与 diagnostics 的
// 内存礼仪一致。
type ServiceStatusEndpointRecorder struct {
	mu        sync.Mutex
	counters  ServiceStatusEndpointCounters
	completed [ServiceStatusSlots]statusCompletionMinute
}

// DefaultServiceStatusEndpoints 是进程内默认的分桶计数记录器。
var DefaultServiceStatusEndpoints = &ServiceStatusEndpointRecorder{}

// ObserveEndpoint 记录一次**已完成**的请求。
//
// route 只用于 ClassifyEndpoint 分类，分类之后原文即弃 —— 本类型不保存、
// 不打印、不持久化任何路由字符串（红线第 3、4 条）。
//
// status >= 400 计为失败（与 diagnostics 的 HTTP 计数口径一致）。
// latencyMillis 为负时按 0 处理（时钟回退）。
func (r *ServiceStatusEndpointRecorder) ObserveEndpoint(route string, status int, latencyMillis int64) {
	r.ObserveCompletion(route, status, latencyMillis, StatusFailureNone, time.Now())
}

// ObserveCompletion records one terminal response at its actual completion time.
// No path, status code or identity is retained in the minute ring.
func (r *ServiceStatusEndpointRecorder) ObserveCompletion(route string, status int, latencyMillis int64, failure StatusFailure, at time.Time) {
	if r == nil {
		return
	}
	kind := ClassifyEndpoint(route)
	at = at.Round(0)
	minute := serviceStatusMinute(at)
	r.mu.Lock()
	defer r.mu.Unlock()
	recordStatusCompletion(r.bucketLocked(kind), status, latencyMillis, failure)
	if minute < 0 {
		return
	}
	slot := &r.completed[minute%ServiceStatusSlots]
	if slot.minute > minute {
		return // A delayed completion must not overwrite a newer ring slot.
	}
	if slot.minute != minute {
		*slot = statusCompletionMinute{minute: minute}
	}
	recordStatusCompletion(&slot.all, status, latencyMillis, failure)
	bucket := &slot.endpoints.Other
	if kind == EndpointSearch {
		bucket = &slot.endpoints.Search
		if at.After(slot.latestSearch) {
			slot.latestSearch = at
		}
	}
	recordStatusCompletion(bucket, status, latencyMillis, failure)
	if at.After(slot.latest) {
		slot.latest = at
	}
}

func recordStatusCompletion(counters *StatusCounters, status int, latencyMillis int64, failure StatusFailure) {
	counters.TotalRequests++
	if status >= 400 {
		counters.FailedRequests++
		observeStatusFailure(counters, failure)
	}
	counters.LatencyCount++
	counters.LatencyTotalMillis += uint64(max(latencyMillis, 0))
}

// bucketLocked 返回指定桶的指针。调用方必须持锁。
func (r *ServiceStatusEndpointRecorder) bucketLocked(kind EndpointKind) *StatusCounters {
	switch kind {
	case EndpointSearch:
		return &r.counters.Search
	default:
		// other 是固定枚举下的唯一兜底；绝不新建第三个桶，
		// 否则「固定两个桶」这条性质失守。
		return &r.counters.Other
	}
}

// Snapshot 返回分桶累计计数的一致视图。nil 记录器返回零值。
func (r *ServiceStatusEndpointRecorder) Snapshot() ServiceStatusEndpointCounters {
	if r == nil {
		return ServiceStatusEndpointCounters{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters
}

// ObserveServiceStatusEndpoint 是 DefaultServiceStatusEndpoints 的包级入口。
//
// 供 HTTP 中间件在**已有的**那处记账点顺手多记一次（138 §4.2 要求「不要新加
// 中间件」—— 多一层中间件会给每个请求加一次开销，而这里复用已有的 measurements）。
func ObserveServiceStatusEndpoint(route string, status int, latencyMillis int64) {
	DefaultServiceStatusEndpoints.ObserveEndpoint(route, status, latencyMillis)
}

// ObserveServiceStatusCompletion is used only at the transport completion boundary.
func ObserveServiceStatusCompletion(route string, status int, latencyMillis int64, failure StatusFailure, at time.Time) {
	DefaultServiceStatusEndpoints.ObserveCompletion(route, status, latencyMillis, failure, at)
}

// endpointCountersDelta 把分桶累计值做差成分钟增量。
// 单桶的饱和差复用 diffStatusCounters，与全局计数器同口径
// （计数回退按 0 处理，不让进程重启把差值翻成巨大数字）。
func endpointCountersDelta(previous, current ServiceStatusEndpointCounters) ServiceStatusEndpointCounters {
	return ServiceStatusEndpointCounters{
		Search: diffStatusCounters(previous.Search, current.Search),
		Other:  diffStatusCounters(previous.Other, current.Other),
	}
}

// applyEndpointDelta 把一个分钟的分桶增量加进点。
//
// sampled 说明「这一分钟确实被采样过」（采样器持有分桶计数源）。它为真时两个桶
// 的 Samples 都置 1 —— **哪怕该桶这一分钟 0 次请求**，因为「0 次请求」本身
// 就是一个观测到的值。
//
// 这一步是 samples 语义的全部要害：若把 samples 改成「TotalRequests>0 的分钟数」，
// 那么「服务在跑、只是没人搜题」（total=0, samples=0）就与「根本没采样」
// （total=0, samples=0）重新同形 —— 而这两件事的用户可见结论完全相反
// （前者正常，后者是服务刚重启）。见 138 §4.2.2。
func applyEndpointDelta(point *StatusPoint, delta ServiceStatusEndpointCounters, sampled bool) {
	applyEndpointBucket(&point.Endpoints.Search, delta.Search, sampled)
	applyEndpointBucket(&point.Endpoints.Other, delta.Other, sampled)
}

// applyEndpointBucket 把一个桶的增量加进点（饱和运算，与全局计数器同口径）。
func applyEndpointBucket(target *EndpointCounters, delta StatusCounters, sampled bool) {
	total := toInt64(delta.TotalRequests)
	failed := toInt64(delta.FailedRequests)
	if failed > total {
		failed = total
	}
	target.TotalRequests = saturatingInt64Add(target.TotalRequests, total)
	target.FailedRequests = saturatingInt64Add(target.FailedRequests, failed)
	if success := total - failed; success > 0 {
		target.SuccessRequests = saturatingInt64Add(target.SuccessRequests, success)
	}
	target.latency.addDelta(delta)
	target.AvgLatencyMs = target.latency.average()
	// 点的 Samples 是 0/1 标记：本分钟是否被采样。窗口聚合时按分钟相加。
	if sampled {
		target.Samples = 1
	}
}

// statusLatencyTally keeps the original sample count and sum across minutes:
// weighting truncated per-minute averages by request total can lose both
// measured zero-duration requests and the fractional remainder.
type statusLatencyTally struct {
	total int64
	count int64
}

func (t *statusLatencyTally) addDelta(delta StatusCounters) {
	if delta.LatencyCount > 0 {
		t.total = saturatingInt64Add(t.total, toInt64(delta.LatencyTotalMillis))
		t.count = saturatingInt64Add(t.count, toInt64(delta.LatencyCount))
	}
}

func (t *statusLatencyTally) merge(point statusLatencyTally, average, requests int64) {
	// Explicit StatusPoint fixtures can provide only an average.
	if point.count == 0 && average > 0 && requests > 0 {
		point = statusLatencyTally{total: saturatingInt64Mul(average, requests), count: requests}
	}
	t.total = saturatingInt64Add(t.total, point.total)
	t.count = saturatingInt64Add(t.count, point.count)
}

func (t statusLatencyTally) average() int64 {
	if t.count == 0 {
		return 0
	}
	return t.total / t.count
}

func mergeEndpointBucket(target *EndpointCounters, point EndpointCounters) {
	target.TotalRequests = saturatingInt64Add(target.TotalRequests, point.TotalRequests)
	target.SuccessRequests = saturatingInt64Add(target.SuccessRequests, point.SuccessRequests)
	target.FailedRequests = saturatingInt64Add(target.FailedRequests, point.FailedRequests)
	target.latency.merge(point.latency, point.AvgLatencyMs, point.TotalRequests)
}

// summarizeEndpointCounters 把分钟点聚合成分桶摘要（138 §4.2.2）。
//
// ⚠️ 入参有两个切片，不是冗余 —— 它们的**口径刻意不同**：
//
//	window  → TotalRequests / SuccessRequests / FailedRequests / AvgLatencyMs
//	all     → Samples（「该桶有值的分钟数」）
//
// 把 samples 也压到 window 会让「10 分钟前有 1000 次搜题、最近 5 分钟没人搜题」
// 显示成「暂无搜题接口采样数据」——而真实情况是服务在正常运行、只是暂时没请求。
// 这两种结论对用户完全相反，所以 samples 必须在整个保留序列上数（最多 1440）。
// 138 §4.2.2 的示例也是这个口径（windowMinutes=5 而 samples=12）。
func summarizeEndpointCounters(all, window []StatusPoint) StatusCountersByEndpoint {
	var result StatusCountersByEndpoint
	for _, point := range window {
		mergeEndpointBucket(&result.Search, point.Endpoints.Search)
		mergeEndpointBucket(&result.Other, point.Endpoints.Other)
	}
	result.Search.AvgLatencyMs = result.Search.latency.average()
	result.Other.AvgLatencyMs = result.Other.latency.average()

	// samples 取各点自带的 Samples 字段（而不是重新用 TotalRequests>0 推），
	// 这样「该桶在这一分钟被采样到」这件事只有一个来源，不会两处漂移。
	for _, point := range all {
		if point.Endpoints.Search.Samples > 0 {
			result.Search.Samples = saturatingInt64Add(result.Search.Samples, 1)
		}
		if point.Endpoints.Other.Samples > 0 {
			result.Other.Samples = saturatingInt64Add(result.Other.Samples, 1)
		}
	}
	return result
}
