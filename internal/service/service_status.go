package service

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/diagnostics"
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
}

// StatusFailure 是失败原因的分类枚举。
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
// PointsUsed 说明实际参与推导的点数：进程刚起时窗口不足 5 点，用已有的点推导。
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
	if point.AvgLatencyMs < 0 {
		point.AvgLatencyMs = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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

// DefaultServiceStatusFailures 是进程内默认的失败归类记录器。
var DefaultServiceStatusFailures = &StatusFailureRecorder{}

// Observe 记录一次失败归类。None 不计数。
func (r *StatusFailureRecorder) Observe(kind StatusFailure) {
	if kind == StatusFailureNone {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case StatusFailureTimeout:
		r.counters.TimeoutFailures++
	case StatusFailureAuth:
		r.counters.AuthFailures++
	case StatusFailureQuota:
		r.counters.QuotaFailures++
	default:
		r.counters.UpstreamFailures++
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

// defaultServiceStatusCounters 从既有基础设施取累计计数：
// 请求总量/失败量来自 diagnostics.Collector（HTTP 中间件已在记账），
// 失败四分类来自 DefaultServiceStatusFailures（业务侧调用 ClassifyServiceStatusFailure 后记账）。
// 返回的是**单调累计值**，由采样器做差得到每分钟增量。
func defaultServiceStatusCounters() StatusCounters {
	snapshot := diagnostics.Default.Snapshot()
	failures := DefaultServiceStatusFailures.Snapshot()
	return StatusCounters{
		TotalRequests:      snapshot.HTTP.Total,
		FailedRequests:     snapshot.HTTP.Failed,
		UpstreamFailures:   failures.UpstreamFailures,
		TimeoutFailures:    failures.TimeoutFailures,
		AuthFailures:       failures.AuthFailures,
		QuotaFailures:      failures.QuotaFailures,
		LatencyCount:       snapshot.HTTP.LatencyCount,
		LatencyTotalMillis: snapshot.HTTP.LatencyTotalMillis,
	}
}

// NewDefaultServiceStatusSampler 构造绑定默认缓冲与默认计数源的采样器。
func NewDefaultServiceStatusSampler() *ServiceStatusSampler {
	return NewServiceStatusSampler(DefaultServiceStatusRing, time.Now, defaultServiceStatusCounters)
}

// StartServiceStatusSampling 每秒采样一次（分钟翻页时才封存），stop 关闭即退出。
//
// 为什么不是每分钟 tick 一次：自然分钟边界可能在 tick 之后很久才到，
// 每秒采样能把「翻页」的检测粒度压到 1 秒内，且聚合仍是按分钟（114 §3.6 N4-k）。
// 采样本身只做两次计数快照的差，成本是 O(1)，与请求量无关。
func StartServiceStatusSampling(sampler *ServiceStatusSampler, stop <-chan struct{}) {
	if sampler == nil {
		return
	}
	go sampler.Run(stop, time.Second)
}

// ClassifyServiceStatusFailure 把一次请求错误归到失败原因上。
// 调用方应同时调用 DefaultServiceStatusFailures.Observe 与 diagnostics 的计数，
// 这样 StatusPoint 的四个失败分类才有数据。
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
// 每次 Sample 先读累计计数、与上次做差，再判断分钟是否翻页；翻页时把上一分钟
// 封存进环形缓冲（只封存有流量的分钟，避免空转分钟把缓冲填成 1440 个零点）。
type ServiceStatusSampler struct {
	ring      *ServiceStatusRing
	now       func() time.Time
	read      func() StatusCounters
	startedAt time.Time

	mu                 sync.Mutex
	last               StatusCounters
	hasLast            bool
	pending            StatusPoint
	hasPending         bool
	pendingLatencySum  int64
	pendingLatencyHits int64
}

// NewServiceStatusSampler 构造采样器。now 为 nil 时用 time.Now；
// read 必须返回单调累计值。
func NewServiceStatusSampler(ring *ServiceStatusRing, now func() time.Time, read func() StatusCounters) *ServiceStatusSampler {
	if ring == nil {
		ring = NewServiceStatusRing()
	}
	if now == nil {
		now = time.Now
	}
	if read == nil {
		read = func() StatusCounters { return StatusCounters{} }
	}
	return &ServiceStatusSampler{ring: ring, now: now, read: read, startedAt: now()}
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
	minute := s.now().Unix() / 60
	current := s.read()
	delta := diffStatusCounters(s.last, current)
	s.last = current
	s.hasLast = true
	if !s.hasPending || s.pending.Minute != minute {
		s.sealLocked()
		s.pending = StatusPoint{Minute: minute}
		s.hasPending = true
	}
	applyStatusDelta(&s.pending, delta)
	s.pendingLatencySum = saturatingInt64Add(s.pendingLatencySum, toInt64(delta.LatencyTotalMillis))
	s.pendingLatencyHits = saturatingInt64Add(s.pendingLatencyHits, toInt64(delta.LatencyCount))
	if s.pendingLatencyHits > 0 {
		s.pending.AvgLatencyMs = s.pendingLatencySum / s.pendingLatencyHits
	}
	return s.pending
}

// Flush 立即封存当前分钟（测试与优雅停机用）。
func (s *ServiceStatusSampler) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealLocked()
}

func (s *ServiceStatusSampler) sealLocked() {
	if !s.hasPending {
		return
	}
	if s.pending.TotalRequests > 0 {
		s.ring.Append(s.pending)
	}
	s.pending = StatusPoint{}
	s.hasPending = false
	s.pendingLatencySum = 0
	s.pendingLatencyHits = 0
}

// Points 返回已封存的历史点（升序），并把「进行中」的那一分钟追加在末尾，
// 使状态页不必等下一分钟才看到曲线。总长度不超过 ServiceStatusSlots。
func (s *ServiceStatusSampler) Points() []StatusPoint {
	s.mu.Lock()
	points := s.ring.Points()
	if s.hasPending && s.pending.TotalRequests > 0 {
		points = append(points, s.pending)
	}
	s.mu.Unlock()
	if len(points) > ServiceStatusSlots {
		points = points[len(points)-ServiceStatusSlots:]
	}
	return points
}

// Summary 用最近 ServiceStatusWindowMinutes 分钟推导当前状态。
func (s *ServiceStatusSampler) Summary() ServiceStatusSummary {
	return SummarizeServiceStatus(s.Points(), ServiceStatusWindowMinutes)
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

// SummarizeServiceStatus 取最后 windowMinutes 个点推导状态（114 §4.2）。
// 点不够时用已有的点推导，PointsUsed 会小于 WindowMinutes。
func SummarizeServiceStatus(points []StatusPoint, windowMinutes int) ServiceStatusSummary {
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
	window := points
	if len(window) > windowMinutes {
		window = window[len(window)-windowMinutes:]
	}
	summary.PointsUsed = len(window)
	summary.StartMinute = window[0].Minute
	summary.EndMinute = window[len(window)-1].Minute

	var latencyWeightedSum int64
	var latencyWeight int64
	for _, point := range window {
		summary.TotalRequests = saturatingInt64Add(summary.TotalRequests, point.TotalRequests)
		summary.SuccessRequests = saturatingInt64Add(summary.SuccessRequests, point.SuccessRequests)
		summary.FailedRequests = saturatingInt64Add(summary.FailedRequests, point.FailedRequests)
		summary.Breakdown.Upstream = saturatingInt64Add(summary.Breakdown.Upstream, point.UpstreamFailures)
		summary.Breakdown.Timeout = saturatingInt64Add(summary.Breakdown.Timeout, point.TimeoutFailures)
		summary.Breakdown.Auth = saturatingInt64Add(summary.Breakdown.Auth, point.AuthFailures)
		summary.Breakdown.Quota = saturatingInt64Add(summary.Breakdown.Quota, point.QuotaFailures)
		if point.TotalRequests > 0 && point.AvgLatencyMs > 0 {
			latencyWeightedSum = saturatingInt64Add(latencyWeightedSum, saturatingInt64Mul(point.AvgLatencyMs, point.TotalRequests))
			latencyWeight = saturatingInt64Add(latencyWeight, point.TotalRequests)
		}
	}
	if latencyWeight > 0 {
		summary.AvgLatencyMs = latencyWeightedSum / latencyWeight
	}
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
