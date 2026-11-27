package service

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/uapi"
)

// ServiceStatusSlots 是环形缓冲的容量：24 小时 × 每分钟 1 个点 = 1440 个槽位。
const ServiceStatusSlots = 1440

// ServiceStatusWindowMinutes 是推导「当前状态」所用的滚动窗口（task_doc/114 §4.2）。
const ServiceStatusWindowMinutes = 5

// 状态阈值（task_doc/114 §4.2）：>=95% healthy，70%~95% degraded，<70% down，无数据 unknown。
const (
	ServiceStatusHealthyThreshold  = 0.95
	ServiceStatusDegradedThreshold = 0.70
)

// 四种当前状态取值。
const (
	ServiceStatusHealthy  = "healthy"
	ServiceStatusDegraded = "degraded"
	ServiceStatusDown     = "down"
	ServiceStatusUnknown  = "unknown"
)

// StatusPoint 是一分钟的聚合结果（task_doc/114 §4.1）。
// Minute 是该点所属的自然分钟（Unix 时间戳 / 60），不是写入时刻。
type StatusPoint struct {
	// Minute 是该点所属的分钟（Unix 时间戳 / 60）。
	Minute int64 `json:"minute"`

	TotalRequests   int64 `json:"totalRequests"`
	SuccessRequests int64 `json:"successRequests"`
	FailedRequests  int64 `json:"failedRequests"`

	// 失败按原因分类，四者之和 <= FailedRequests（未归类的失败落 UpstreamFailures）。
	UpstreamFailures int64 `json:"upstreamFailures"`
	TimeoutFailures  int64 `json:"timeoutFailures"`
	AuthFailures     int64 `json:"authFailures"`
	QuotaFailures    int64 `json:"quotaFailures"`

	// AvgLatencyMs 是该分钟内请求的平均耗时。
	AvgLatencyMs int64 `json:"avgLatencyMs"`

	// Internal observation metadata is not part of either public projection.
	sampledAt       time.Time
	searchSampledAt time.Time
	latency         statusLatencyTally
	process         *StatusProcessPoint
	eventOnly       bool
	requestUnknown  bool

	// Endpoints 是该分钟按接口族（search / other）分桶的聚合值（138 §4.2）。
	//
	// 分桶是**加法信息**：两个桶的 TotalRequests 之和等于本点的 TotalRequests，
	// 但它多回答了「搜题链路自己现在健康吗」—— 全局成功率被大量非搜题请求
	// （健康检查、管理页、静态资源）稀释，出问题时会被拉高到看不出异常。
	//
	// 固定两个桶，不含任何路由字符串：见 service_status_endpoint.go 的红线说明。
	Endpoints StatusCountersByEndpoint `json:"endpoints"`
}

// StatusFailure 是失败原因的分类枚举。
// PeriodicallyObserved distinguishes an actual sampler tick from a minute
// evidenced solely by completed requests. Legacy public site traffic uses ticks only.
func (p StatusPoint) PeriodicallyObserved() bool { return !p.eventOnly && !p.requestUnknown }

type StatusFailure int

const (
	StatusFailureNone StatusFailure = iota
	StatusFailureUpstream
	StatusFailureTimeout
	StatusFailureAuth
	StatusFailureQuota
)

// ServiceStatusBreakdown 是最近窗口内失败原因的分解。
type ServiceStatusBreakdown struct {
	Upstream int64 `json:"upstream"`
	Timeout  int64 `json:"timeout"`
	Auth     int64 `json:"auth"`
	Quota    int64 `json:"quota"`
}

// ServiceStatusSummary 是「当前状态」的推导结果。
// PointsUsed 是当前 5 个自然分钟内实际采样的点数；重启和缺口不补点。
type ServiceStatusSummary struct {
	Status          string                 `json:"status"`
	SuccessRate     float64                `json:"successRate"`
	AvgLatencyMs    int64                  `json:"avgLatencyMs"`
	TotalRequests   int64                  `json:"totalRequests"`
	SuccessRequests int64                  `json:"successRequests"`
	FailedRequests  int64                  `json:"failedRequests"`
	Breakdown       ServiceStatusBreakdown `json:"breakdown"`
	WindowMinutes   int                    `json:"windowMinutes"`
	PointsUsed      int                    `json:"pointsUsed"`
	StartMinute     int64                  `json:"startMinute"`
	EndMinute       int64                  `json:"endMinute"`

	// Endpoints 是按接口族分桶的聚合（138 §4.2.2）。
	//
	// ⚠️ 两个桶的**统计口径刻意不同**，这是本字段唯一的坑：
	//
	//	TotalRequests / SuccessRequests / FailedRequests / AvgLatencyMs
	//	    → 最近 WindowMinutes 分钟（与顶层同口径，可直接对比）
	//	Samples
	//	    → **整个保留序列**（最多 1440 分钟）里该桶确实被采样的分钟数
	//
	// 为什么不统一成窗口口径：samples 的职责是回答「这个桶到底有没有数据」，
	// 而不是「最近 5 分钟有多少数据」。若也压到 5 分钟，那么「10 分钟前有
	// 1000 次搜题、最近 5 分钟没有」就会被显示成「暂无搜题接口采样数据」——
	// 而那恰恰是「服务在运行、只是暂时没人搜题」，两种情况的用户可见结论相反。
	// 138 §4.2.2 的示例也是这个口径（windowMinutes=5 而 samples=12）。
	Endpoints StatusCountersByEndpoint `json:"endpoints"`
}

// ServiceStatusRing 是服务状态的**内存环形缓冲**，定长 1440 槽（约 115 KB）。
//
// 为什么不写数据库（task_doc/114 §3.2，用户已定）：
//  1. 这是一天的滚动窗口，重启后从零开始是可接受的（114 §3.5）—— 状态页的价值是
//     「看到最近发生了什么」，不是「永久审计」。
//  2. 每分钟写一次库，一天 1440 行、一年 52 万行 —— 纯粹是垃圾数据。
//  3. 内存占用仅 115 KB。
//
// 为什么用环形缓冲而不是 map：定长数组天然实现「滚动删除」—— 写入位置取模，
// 旧值被自动覆盖，不需要任何后台清理任务（114 §3.4）。
type ServiceStatusRing struct {
	mu sync.RWMutex

	// slots 是定长 1440 的环形数组（1 天 × 每分钟 1 个点）。
	slots [ServiceStatusSlots]StatusPoint
	// head 是下一次写入位置。
	head int
	// count 是已填充的槽位数（<= ServiceStatusSlots）。
	count int
}

// NewServiceStatusRing 返回一个空环形缓冲。空缓冲的 Points() 是长度 0 的切片，
// **不会**返回 1440 个零点。
func NewServiceStatusRing() *ServiceStatusRing {
	return &ServiceStatusRing{}
}

// Append 把一个点写入当前 head，然后 head 前进一格（取模回绕）。
// 缓冲写满后，最老的点被新点覆盖 —— 这就是「滚动删除」，不是追加。
func (r *ServiceStatusRing) Append(point StatusPoint) {
	r.write(point, false)
}

func (r *ServiceStatusRing) write(point StatusPoint, replaceLatest bool) {
	if point.AvgLatencyMs < 0 {
		point.AvgLatencyMs = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if replaceLatest && r.count > 0 {
		last := (r.head + ServiceStatusSlots - 1) % ServiceStatusSlots
		if r.slots[last].Minute == point.Minute {
			r.slots[last] = point
			return
		}
	}
	r.slots[r.head] = point
	r.head = (r.head + 1) % ServiceStatusSlots
	if r.count < ServiceStatusSlots {
		r.count++
	}
}

// Len 返回已填充的槽位数。
func (r *ServiceStatusRing) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Capacity 返回固定容量（ServiceStatusSlots）。
func (r *ServiceStatusRing) Capacity() int {
	return ServiceStatusSlots
}

// Points 返回按时间升序（最老 → 最新）展开的点，长度等于已填充槽位数。
// 空缓冲返回**非 nil 的空切片**，序列化成 [] 而不是 null，也不是 1440 个零点。
func (r *ServiceStatusRing) Points() []StatusPoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	points := make([]StatusPoint, 0, r.count)
	if r.count == 0 {
		return points
	}
	start := r.head - r.count
	if start < 0 {
		start += ServiceStatusSlots
	}
	for i := 0; i < r.count; i++ {
		points = append(points, r.slots[(start+i)%ServiceStatusSlots])
	}
	return points
}

// StatusCounters 是采样源的**单调累计**计数快照。
// 采样器相邻两次采样做差，得到「这一分钟发生了什么」；直接用累计值会把曲线
// 画成永远上升的总量。
type StatusCounters struct {
	TotalRequests      uint64
	FailedRequests     uint64
	UpstreamFailures   uint64
	TimeoutFailures    uint64
	AuthFailures       uint64
	QuotaFailures      uint64
	LatencyCount       uint64
	LatencyTotalMillis uint64
}

// StatusFailureRecorder 记录失败归类计数（进程内单调累计）。
// 采样器只做差，所以这里必须是累计值而不是每分钟值。
type StatusFailureRecorder struct {
	mu       sync.Mutex
	counters StatusCounters
}

// Observe 记录一次失败归类。None 不计数。
func (r *StatusFailureRecorder) Observe(kind StatusFailure) {
	if kind == StatusFailureNone {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	observeStatusFailure(&r.counters, kind)
}

func observeStatusFailure(counters *StatusCounters, kind StatusFailure) {
	switch kind {
	case StatusFailureTimeout:
		counters.TimeoutFailures++
	case StatusFailureAuth:
		counters.AuthFailures++
	case StatusFailureQuota:
		counters.QuotaFailures++
	case StatusFailureUpstream:
		counters.UpstreamFailures++
	}
}

// Snapshot 返回累计的失败分解（其他字段为零，采样器只关心分解四项）。
func (r *StatusFailureRecorder) Snapshot() StatusCounters {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters
}

// DefaultServiceStatusRing 是进程内默认的状态环形缓冲。
//
// 重启归零是明确接受的：见上方 ServiceStatusRing 注释的第 1 条理由（114 §3.5）。
var DefaultServiceStatusRing = NewServiceStatusRing()

// NewDefaultServiceStatusSampler uses the same completed HTTP observations
// for total, outcomes, endpoint families and latency. The diagnostics collector
// retains its separate started/in-flight semantics.
func NewDefaultServiceStatusSampler() *ServiceStatusSampler {
	return NewServiceStatusSamplerWithSources(DefaultServiceStatusRing, time.Now, ServiceStatusSources{
		Completions: DefaultServiceStatusEndpoints,
		Process:     readServiceStatusProcess,
	})
}

// StartServiceStatusSampling 每秒采样一次（分钟翻页时才封存），stop 关闭即退出。
//
// 为什么不是每分钟 tick 一次：自然分钟边界可能在 tick 之后很久才到，
// 每秒采样能把「翻页」的检测粒度压到 1 秒内，且聚合仍是按分钟（114 §3.6 N4-k）。
// 每秒更新当前分钟的覆盖；管理员进程诊断每个实际采样分钟只读一次。
func StartServiceStatusSampling(sampler *ServiceStatusSampler, stop <-chan struct{}) {
	if sampler == nil {
		return
	}
	go sampler.Run(stop, time.Second)
}

// ClassifyServiceStatusFailure 把一次请求错误归到失败原因上。
// 只在终态错误来源已知时使用；请求的其余计数在 transport 完成时一次写入。
func ClassifyServiceStatusFailure(err error) StatusFailure {
	if err == nil {
		return StatusFailureNone
	}
	if isTimeoutError(err) {
		return StatusFailureTimeout
	}
	switch {
	case errors.Is(err, uapi.ErrInsufficientCredits), errors.Is(err, uapi.ErrRateLimited):
		return StatusFailureQuota
	case errors.Is(err, uapi.ErrUnauthorized), errors.Is(err, uapi.ErrForbidden):
		return StatusFailureAuth
	default:
		return StatusFailureUpstream
	}
}

// ClassifyServiceStatusHTTPStatus recognizes only explicit terminal semantics.
// A generic 4xx/5xx is still a failed request but need not have a cause category.
func ClassifyServiceStatusHTTPStatus(status int) StatusFailure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return StatusFailureAuth
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return StatusFailureTimeout
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return StatusFailureQuota
	default:
		return StatusFailureNone
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, uapi.ErrTimeout) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// ServiceStatusSampler 按自然分钟把增量流量聚合成一个 StatusPoint。
//
// 采样语义（114 §3.6 N4-k）：自然分钟边界，不是「每 60 秒滑一次」。
// Sample records duration-independent coverage at each tick; production
// traffic is joined from completed observations in their actual minute.
type ServiceStatusSampler struct {
	ring      *ServiceStatusRing
	now       func() time.Time
	read      func() StatusCounters
	startedAt time.Time

	// readEndpoints 是分桶累计计数源（138 §4.2）。nil 表示「本采样器不分桶」——
	// 那时每个点的 Endpoints 保持零值，samples 恒为 0，前端据此显灰。
	// 保留 nil 分支是为了不让既有三参数调用方（含测试）被迫改造。
	readEndpoints func() ServiceStatusEndpointCounters
	readProcess   func() StatusProcessMetrics
	completions   *ServiceStatusEndpointRecorder

	mu            sync.Mutex
	last          StatusCounters
	lastEndpoints ServiceStatusEndpointCounters
	pending       StatusPoint
	hasPending    bool
}

// NewServiceStatusSampler 构造采样器。now 为 nil 时用 time.Now；
// read 必须返回单调累计值。
func NewServiceStatusSampler(ring *ServiceStatusRing, now func() time.Time, read func() StatusCounters) *ServiceStatusSampler {
	return NewServiceStatusSamplerWithEndpoints(ring, now, read, nil)
}

// NewServiceStatusSamplerWithEndpoints 构造带分桶计数源的采样器。
//
// readEndpoints 为 nil 时退化成「不分桶」的采样器（Endpoints 零值）——
// 这条退化路径是刻意的：分桶是加法能力，缺了它不该让状态页整体失效。
func NewServiceStatusSamplerWithEndpoints(ring *ServiceStatusRing, now func() time.Time,
	read func() StatusCounters, readEndpoints func() ServiceStatusEndpointCounters) *ServiceStatusSampler {
	return NewServiceStatusSamplerWithSources(ring, now, ServiceStatusSources{Requests: read, Endpoints: readEndpoints})
}

// ServiceStatusSources defines the independent observation sources.
type ServiceStatusSources struct {
	Requests    func() StatusCounters
	Endpoints   func() ServiceStatusEndpointCounters
	Process     func() StatusProcessMetrics
	Completions *ServiceStatusEndpointRecorder // Time-stamped source; takes precedence over counters.
}

func NewServiceStatusSamplerWithSources(ring *ServiceStatusRing, now func() time.Time, sources ServiceStatusSources) *ServiceStatusSampler {
	if ring == nil {
		ring = NewServiceStatusRing()
	}
	if now == nil {
		now = time.Now
	}
	if sources.Requests == nil {
		sources.Requests = func() StatusCounters { return StatusCounters{} }
	}
	return &ServiceStatusSampler{
		ring: ring, now: now, startedAt: now().Round(0), read: sources.Requests,
		readEndpoints: sources.Endpoints, readProcess: sources.Process, completions: sources.Completions,
	}
}

// StartedAt 返回采样器开始记录的时刻（≈ 进程启动时刻）。
// 缓冲纯内存、重启归零，所以这是曲线可信的起点（114 §3.5 要求 UI 标注它）。
func (s *ServiceStatusSampler) StartedAt() time.Time {
	return s.startedAt
}

// Ring 返回底层环形缓冲（只读用途：Len / Points / Capacity）。
func (s *ServiceStatusSampler) Ring() *ServiceStatusRing {
	return s.ring
}

// Sample 采一次样，返回当前（进行中的）分钟聚合值。
// 分钟翻页时先把上一分钟封存进环形缓冲。
func (s *ServiceStatusSampler) Sample() StatusPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.now().Round(0)
	// A backward wall clock must not insert a duplicate or a future observation.
	if at.Before(s.startedAt) || (s.hasPending && at.Before(s.pending.sampledAt)) {
		return s.pending
	}
	minute := serviceStatusMinute(at)
	discontinuous := s.hasPending && minute > s.pending.Minute+1
	if !s.hasPending || s.pending.Minute != minute {
		s.sealLocked()
		s.pending = StatusPoint{Minute: minute}
		s.hasPending = true
		if s.readProcess != nil {
			s.pending.process = &StatusProcessPoint{Minute: minute, SampledAt: at.UTC(), StatusProcessMetrics: s.readProcess()}
		}
	}
	s.pending.sampledAt = at
	if s.completions != nil {
		// A real tick establishes coverage, not an invented completed request.
		s.pending.Endpoints.Search.Samples = 1
		s.pending.Endpoints.Other.Samples = 1
		s.pending.searchSampledAt = at
		events := s.completions.completedBetween(minute, minute)
		if len(events) != 0 {
			if events[0].latest.After(at) {
				return StatusPoint{Minute: minute, sampledAt: at, requestUnknown: true}
			}
			return withStatusCompletions(s.pending, events[0])
		}
		return s.pending
	}

	// Compatibility for an untimed cumulative source. A delta spanning
	// skipped minutes has no trustworthy attribution, even on the resume tick.
	current := s.read()
	delta := diffStatusCounters(s.last, current)
	s.last = current
	var endpointDelta ServiceStatusEndpointCounters
	sampled := s.readEndpoints != nil
	if sampled {
		currentEndpoints := s.readEndpoints()
		endpointDelta = endpointCountersDelta(s.lastEndpoints, currentEndpoints)
		s.lastEndpoints = currentEndpoints
	}
	s.pending.requestUnknown = discontinuous &&
		(delta != (StatusCounters{}) || endpointDelta != (ServiceStatusEndpointCounters{}))
	if s.pending.requestUnknown {
		return s.pending
	}
	applyStatusDelta(&s.pending, delta)
	applyEndpointDelta(&s.pending, endpointDelta, sampled)
	if sampled {
		s.pending.searchSampledAt = at
	}
	s.pending.latency.addDelta(delta)
	s.pending.AvgLatencyMs = s.pending.latency.average()
	return s.pending
}

// Flush 立即封存当前分钟（测试与优雅停机用）。
func (s *ServiceStatusSampler) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealLocked()
}

func (s *ServiceStatusSampler) sealLocked() {
	if s.hasPending {
		s.ring.write(s.pending, true)
	}
}

// ServiceStatusSnapshot is a single read of the clock and bounded observation
// history. Missing minutes are absent; zero-request observations remain.
type ServiceStatusSnapshot struct {
	AsOf                 time.Time
	RestartedAt          time.Time
	LatestSampleAt       time.Time
	LatestSearchSampleAt time.Time
	Points               []StatusPoint
	Summary              ServiceStatusSummary
	processPoints        []StatusProcessPoint
}

func (s *ServiceStatusSampler) Snapshot() ServiceStatusSnapshot {
	s.mu.Lock()
	var asOf time.Time
	var events []statusCompletionMinute
	if s.completions != nil {
		asOf, events = s.completions.completionSnapshot(s.startedAt, s.now)
	} else {
		asOf = s.now().UTC()
	}
	points := s.ring.Points()
	if s.hasPending {
		if last := len(points) - 1; last >= 0 && points[last].Minute == s.pending.Minute {
			points[last] = s.pending // Flush may already have saved this minute.
		} else {
			points = append(points, s.pending)
		}
	}
	start := max(serviceStatusMinute(s.startedAt), serviceStatusMinute(asOf)-ServiceStatusSlots+1)
	end := serviceStatusMinute(asOf)
	s.mu.Unlock()

	history := points[:0]
	result := ServiceStatusSnapshot{
		AsOf: asOf, RestartedAt: s.startedAt, processPoints: make([]StatusProcessPoint, 0, len(points)),
	}
	for _, point := range points {
		if point.Minute < start || point.Minute > end {
			continue
		}
		if point.process != nil && !point.process.SampledAt.After(asOf) {
			result.processPoints = append(result.processPoints, *point.process)
		}
		if !point.requestUnknown && !point.sampledAt.After(asOf) {
			history = append(history, point)
		}
	}
	if len(events) > 0 {
		history = mergeStatusCompletions(history, events, asOf)
	}
	for _, point := range history {
		if point.sampledAt.After(result.LatestSampleAt) {
			result.LatestSampleAt = point.sampledAt.UTC()
		}
		if point.searchSampledAt.After(result.LatestSearchSampleAt) {
			result.LatestSearchSampleAt = point.searchSampledAt.UTC()
		}
	}
	result.Points = history
	result.Summary = summarizeServiceStatusAt(history, end, ServiceStatusWindowMinutes)
	return result
}

// LegacyTrafficSummary preserves the scope of public site-wide aggregate
// compatibility fields. Previously unseen completion-only minutes are used by
// the dedicated search and admin views, not to expand the legacy site view.
func (s ServiceStatusSnapshot) LegacyTrafficSummary() ServiceStatusSummary {
	legacy := make([]StatusPoint, 0, len(s.Points))
	for _, point := range s.Points {
		if point.PeriodicallyObserved() && point.TotalRequests > 0 {
			legacy = append(legacy, point)
		}
	}
	return summarizeServiceStatusAt(legacy, serviceStatusMinute(s.AsOf), ServiceStatusWindowMinutes)
}

func serviceStatusMinute(at time.Time) int64 {
	return at.Truncate(time.Minute).Unix() / 60
}

// Points returns the current bounded history in chronological order.
func (s *ServiceStatusSampler) Points() []StatusPoint {
	return s.Snapshot().Points
}

// Summary derives current health from actual observations within the latest five natural minutes.
func (s *ServiceStatusSampler) Summary() ServiceStatusSummary {
	return s.Snapshot().Summary
}

// Run 是唯一的采样循环。interval <= 0 时用 1 分钟。
func (s *ServiceStatusSampler) Run(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.Sample()
		}
	}
}

// SummarizeServiceStatus uses the last point as the clock for explicit history.
// Live callers use Snapshot, where the clock does not stop on a gap.
func SummarizeServiceStatus(points []StatusPoint, windowMinutes int) ServiceStatusSummary {
	var end int64
	for _, point := range points {
		end = max(end, point.Minute)
	}
	return summarizeServiceStatusAt(points, end, windowMinutes)
}

func summarizeServiceStatusAt(points []StatusPoint, end int64, windowMinutes int) ServiceStatusSummary {
	if windowMinutes <= 0 {
		windowMinutes = ServiceStatusWindowMinutes
	}
	summary := ServiceStatusSummary{
		Status:        ServiceStatusUnknown,
		WindowMinutes: windowMinutes,
	}
	if len(points) == 0 {
		return summary
	}
	start := end - int64(windowMinutes) + 1
	window := make([]StatusPoint, 0, min(len(points), windowMinutes))
	for _, point := range points {
		if point.Minute >= start && point.Minute <= end {
			window = append(window, point)
		}
	}
	// 分桶：计数限 5 自然分钟，samples 数整个保留窗口。
	summary.Endpoints = summarizeEndpointCounters(points, window)
	if len(window) == 0 {
		return summary
	}
	summary.PointsUsed = len(window)
	summary.StartMinute = window[0].Minute
	summary.EndMinute = window[len(window)-1].Minute

	var latency statusLatencyTally
	for _, point := range window {
		summary.TotalRequests = saturatingInt64Add(summary.TotalRequests, point.TotalRequests)
		summary.SuccessRequests = saturatingInt64Add(summary.SuccessRequests, point.SuccessRequests)
		summary.FailedRequests = saturatingInt64Add(summary.FailedRequests, point.FailedRequests)
		summary.Breakdown.Upstream = saturatingInt64Add(summary.Breakdown.Upstream, point.UpstreamFailures)
		summary.Breakdown.Timeout = saturatingInt64Add(summary.Breakdown.Timeout, point.TimeoutFailures)
		summary.Breakdown.Auth = saturatingInt64Add(summary.Breakdown.Auth, point.AuthFailures)
		summary.Breakdown.Quota = saturatingInt64Add(summary.Breakdown.Quota, point.QuotaFailures)
		latency.merge(point.latency, point.AvgLatencyMs, point.TotalRequests)
	}
	summary.AvgLatencyMs = latency.average()
	// 窗口内一次请求都没有 → 无数据，不是 100%。
	if summary.TotalRequests <= 0 {
		summary.Status = ServiceStatusUnknown
		return summary
	}
	summary.SuccessRate = float64(summary.SuccessRequests) / float64(summary.TotalRequests)
	switch {
	case summary.SuccessRate >= ServiceStatusHealthyThreshold:
		summary.Status = ServiceStatusHealthy
	case summary.SuccessRate >= ServiceStatusDegradedThreshold:
		summary.Status = ServiceStatusDegraded
	default:
		summary.Status = ServiceStatusDown
	}
	return summary
}

// diffStatusCounters 做饱和差：计数回退（进程重启、测试夹具重置）时按 0 处理，
// 不让差值翻成巨大的负数/正数。
func diffStatusCounters(previous, current StatusCounters) StatusCounters {
	return StatusCounters{
		TotalRequests:      subtractSaturated(current.TotalRequests, previous.TotalRequests),
		FailedRequests:     subtractSaturated(current.FailedRequests, previous.FailedRequests),
		UpstreamFailures:   subtractSaturated(current.UpstreamFailures, previous.UpstreamFailures),
		TimeoutFailures:    subtractSaturated(current.TimeoutFailures, previous.TimeoutFailures),
		AuthFailures:       subtractSaturated(current.AuthFailures, previous.AuthFailures),
		QuotaFailures:      subtractSaturated(current.QuotaFailures, previous.QuotaFailures),
		LatencyCount:       subtractSaturated(current.LatencyCount, previous.LatencyCount),
		LatencyTotalMillis: subtractSaturated(current.LatencyTotalMillis, previous.LatencyTotalMillis),
	}
}

func applyStatusDelta(point *StatusPoint, delta StatusCounters) {
	total := toInt64(delta.TotalRequests)
	failed := toInt64(delta.FailedRequests)
	if failed > total {
		failed = total
	}
	point.TotalRequests = saturatingInt64Add(point.TotalRequests, total)
	point.FailedRequests = saturatingInt64Add(point.FailedRequests, failed)
	if success := total - failed; success > 0 {
		point.SuccessRequests = saturatingInt64Add(point.SuccessRequests, success)
	}
	point.UpstreamFailures = saturatingInt64Add(point.UpstreamFailures, toInt64(delta.UpstreamFailures))
	point.TimeoutFailures = saturatingInt64Add(point.TimeoutFailures, toInt64(delta.TimeoutFailures))
	point.AuthFailures = saturatingInt64Add(point.AuthFailures, toInt64(delta.AuthFailures))
	point.QuotaFailures = saturatingInt64Add(point.QuotaFailures, toInt64(delta.QuotaFailures))
}

func subtractSaturated(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func toInt64(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}

func saturatingInt64Add(a, b int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if b > 0 && a > maxInt64-b {
		return maxInt64
	}
	if b < 0 && a < minInt64-b {
		return minInt64
	}
	return a + b
}

func saturatingInt64Mul(a, b int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if a == 0 || b == 0 {
		return 0
	}
	if a > 0 && b > 0 && a > maxInt64/b {
		return maxInt64
	}
	if a < 0 && b < 0 {
		return maxInt64
	}
	if a < 0 || b < 0 {
		return -saturatingInt64Mul(absInt64(a), absInt64(b))
	}
	return a * b
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
