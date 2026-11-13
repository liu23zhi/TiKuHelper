package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/uapi"
)

// TestServiceStatusRingWrapsAndOverwritesOldest 是判据 ①：
// 写满 1441 次后最老的点被**覆盖**，不是追加。
//
// 断言刻意分散在四个维度上，避免「只证明长度对」的薄断言：
//   - Len 恒等于容量，不会涨到 1441（不是追加）
//   - 最老的点（minute=0）在整个缓冲里彻底消失
//   - 首元素是 minute=1、末元素是 minute=1440（滚动后的正确顺序）
//   - 第 1441 个点真的落进了原来存 minute=0 的那个物理槽位
func TestServiceStatusRingWrapsAndOverwritesOldest(t *testing.T) {
	ring := NewServiceStatusRing()

	for minute := int64(0); minute <= ServiceStatusSlots; minute++ {
		ring.Append(StatusPoint{Minute: minute, TotalRequests: 1})
	}
	if got := ring.Len(); got != ServiceStatusSlots {
		t.Fatalf("写满后 Len 必须是 %d（不是追加），got=%d", ServiceStatusSlots, got)
	}

	points := ring.Points()
	if len(points) != ServiceStatusSlots {
		t.Fatalf("Points 长度必须是 %d，got=%d", ServiceStatusSlots, len(points))
	}
	// 最老的点必须被覆盖掉，而不是仍留在缓冲里。
	for _, point := range points {
		if point.Minute == 0 {
			t.Fatal("minute=0 是最老的点，写满后必须被覆盖，但它仍在 Points() 里")
		}
	}
	// 覆盖后顺序必须仍是升序：首 = 第 2 个写入的点，末 = 第 1441 个写入的点。
	if points[0].Minute != 1 {
		t.Fatalf("首元素必须是 minute=1（原 slots[0] 已被覆盖），got=%d", points[0].Minute)
	}
	if points[len(points)-1].Minute != int64(ServiceStatusSlots) {
		t.Fatalf("末元素必须是 minute=%d，got=%d", ServiceStatusSlots, points[len(points)-1].Minute)
	}
	for i := 1; i < len(points); i++ {
		if points[i].Minute != points[i-1].Minute+1 {
			t.Fatalf("Points 必须严格升序，位置 %d：%d 之后是 %d", i, points[i-1].Minute, points[i].Minute)
		}
	}
	// 第 1441 个点确实落在原 slots[0]，不是落在某个新槽位。
	ring.mu.RLock()
	if got := ring.slots[0].Minute; got != int64(ServiceStatusSlots) {
		ring.mu.RUnlock()
		t.Fatalf("第 1441 个点必须覆盖 slots[0]，slots[0].Minute 期望 %d，got=%d", ServiceStatusSlots, got)
	}
	if got := ring.head; got != 1 {
		ring.mu.RUnlock()
		t.Fatalf("回绕后 head 必须归位到 1，got=%d", got)
	}
	ring.mu.RUnlock()
}

// TestServiceStatusRingIncrementalFillKeepsHeadAligned 覆盖「写满前」的边界：
// 未写满时 head 前进但不回绕，Points 从 slots[0] 开始读。
func TestServiceStatusRingIncrementalFillKeepsHeadAligned(t *testing.T) {
	ring := NewServiceStatusRing()
	for minute := int64(0); minute < 3; minute++ {
		ring.Append(StatusPoint{Minute: minute})
	}
	if ring.Len() != 3 {
		t.Fatalf("Len 期望 3，got=%d", ring.Len())
	}
	points := ring.Points()
	want := []int64{0, 1, 2}
	if len(points) != len(want) {
		t.Fatalf("len=%d", len(points))
	}
	for i, minute := range want {
		if points[i].Minute != minute {
			t.Fatalf("位置 %d 期望 %d，got=%d", i, minute, points[i].Minute)
		}
	}
}

// TestServiceStatusRingEmptyReturnsEmptyNotZeros 是判据 ③：
// 空缓冲返回空，而不是 1440 个零点。
func TestServiceStatusRingEmptyReturnsEmptyNotZeros(t *testing.T) {
	ring := NewServiceStatusRing()
	points := ring.Points()
	if points == nil {
		t.Fatal("空缓冲必须返回非 nil 的空切片（序列化成 [] 而不是 null）")
	}
	if len(points) != 0 {
		t.Fatalf("空缓冲必须返回长度 0，got=%d（1440 个零点就是这条判据要挡的缺陷）", len(points))
	}
	raw, err := json.Marshal(points)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("空缓冲必须序列化成 []，got=%s", raw)
	}
	// 采样器层面的同一条判据：未采样过的采样器 Points/Summary 都不能伪造零点。
	sampler := NewServiceStatusSampler(NewServiceStatusRing(), nil, nil)
	if got := sampler.Points(); len(got) != 0 {
		t.Fatalf("未采样的采样器 Points 必须为空，got=%d", len(got))
	}
	if summary := sampler.Summary(); summary.Status != ServiceStatusUnknown || summary.TotalRequests != 0 {
		t.Fatalf("未采样的采样器 Summary 必须是 unknown/0，got=%+v", summary)
	}
}

// TestServiceStatusRingConcurrentReadWrite 是判据 ②：100 goroutine 并发读写。
// 用 -race 跑；这里同时用「无撕裂读」做行为级断言，因为 -race 只能证明
// 没有数据竞争，不能证明读到的东西是完整的一整条记录。
//
// 关键设计：所有写者写 Minute = id+1 (>= 1)，TotalRequests 恒为 1。
// 于是「读到 Minute==0」或「读到 TotalRequests != 1」就一定是：
//   - 读到了尚未写入的槽位（count 与 slots 不一致），或
//   - 读到半个点（撕裂）
//
// 注意：并发下写入顺序本来就交错，**不能**断言 Minute 升序 —— 那是单写者才成立的
// 性质。环形缓冲保证的是「按写入顺序展开」，不是「按 Minute 排序」。
func TestServiceStatusRingConcurrentReadWrite(t *testing.T) {
	ring := NewServiceStatusRing()
	const goroutines = 100
	const writesPerWriter = 50
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				for round := 0; round < writesPerWriter; round++ {
					ring.Append(StatusPoint{Minute: int64(id + 1), TotalRequests: 1, SuccessRequests: 1})
				}
				return
			}
			for round := 0; round < writesPerWriter; round++ {
				points := ring.Points()
				if len(points) > ring.Capacity() {
					errs <- fmt.Errorf("Points 超过容量: %d", len(points))
					return
				}
				for _, point := range points {
					if point.Minute == 0 {
						errs <- fmt.Errorf("读到未写入的槽位（零值点），count 与 slots 不一致")
						return
					}
					if point.TotalRequests != 1 || point.SuccessRequests != 1 {
						errs <- fmt.Errorf("读到撕裂的点: %+v", point)
						return
					}
				}
				if ring.Len() > ring.Capacity() {
					errs <- fmt.Errorf("Len 超过容量: %d", ring.Len())
					return
				}
				_ = ring.Capacity()
				_ = SummarizeServiceStatus(points, ServiceStatusWindowMinutes)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := ring.Len(); got != ServiceStatusSlots {
		t.Fatalf("%d 个写者 × %d 次 > 容量，Len 必须封顶在 %d，got=%d",
			goroutines/2, writesPerWriter, ServiceStatusSlots, got)
	}
	// 写满后内容必须是最后 1440 次写入的一部分：全部仍是合法点，无零值。
	for _, point := range ring.Points() {
		if point.Minute == 0 || point.TotalRequests != 1 {
			t.Fatalf("写满后仍有非法点: %+v", point)
		}
	}
}

// TestSummarizeServiceStatusThresholds 钉 114 §4.2 的三档阈值与 unknown。
// 期望值全部写字面量：常量漂移时必须变红。
func TestSummarizeServiceStatusThresholds(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		success int64
		want    string
	}{
		{"恰好 95% 是 healthy", 100, 95, "healthy"},
		{"96% 是 healthy", 100, 96, "healthy"},
		{"94% 是 degraded", 100, 94, "degraded"},
		{"恰好 70% 是 degraded", 100, 70, "degraded"},
		{"69% 是 down", 100, 69, "down"},
		{"全部失败是 down", 100, 0, "down"},
		{"零请求是 unknown", 0, 0, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := SummarizeServiceStatus([]StatusPoint{{
				Minute: 1, TotalRequests: tc.total, SuccessRequests: tc.success,
				FailedRequests: tc.total - tc.success,
			}}, ServiceStatusWindowMinutes)
			if summary.Status != tc.want {
				t.Fatalf("total=%d success=%d 期望 status=%q，got=%q rate=%v",
					tc.total, tc.success, tc.want, summary.Status, summary.SuccessRate)
			}
		})
	}
	if ServiceStatusHealthyThreshold != 0.95 || ServiceStatusDegradedThreshold != 0.70 {
		t.Fatalf("阈值常量必须是字面量 0.95 / 0.70，got=%v / %v",
			ServiceStatusHealthyThreshold, ServiceStatusDegradedThreshold)
	}
}

// TestSummarizeServiceStatusUsesLastFiveMinutes 复现 114 §4.3 的具体例子：
// 最近 5 分钟 105 次请求、成功 58 次 = 55.2% → down。
func TestSummarizeServiceStatusUsesLastFiveMinutes(t *testing.T) {
	points := []StatusPoint{
		{Minute: 100, TotalRequests: 21, SuccessRequests: 21, AvgLatencyMs: 210},
		{Minute: 101, TotalRequests: 20, SuccessRequests: 20, AvgLatencyMs: 198},
		{Minute: 102, TotalRequests: 22, SuccessRequests: 11, FailedRequests: 11, AvgLatencyMs: 892},
		{Minute: 103, TotalRequests: 19, SuccessRequests: 4, FailedRequests: 15, AvgLatencyMs: 1203},
		{Minute: 104, TotalRequests: 23, SuccessRequests: 2, FailedRequests: 21, AvgLatencyMs: 1890},
	}
	summary := SummarizeServiceStatus(points, ServiceStatusWindowMinutes)
	if summary.TotalRequests != 105 || summary.SuccessRequests != 58 {
		t.Fatalf("合计期望 105/58，got=%d/%d", summary.TotalRequests, summary.SuccessRequests)
	}
	if summary.Status != "down" {
		t.Fatalf("55.2%% 必须推导为 down，got=%q", summary.Status)
	}
	if summary.PointsUsed != 5 || summary.StartMinute != 100 || summary.EndMinute != 104 {
		t.Fatalf("窗口元信息错: %+v", summary)
	}
	// 114 §4.3 的加权延迟：(210*21 + 198*20 + 892*22 + 1203*19 + 1890*23) / 105 = 898。
	// 算术平均是 878 —— 两者只差 20，用这组数据分辨不出「是否加权」，所以下面
	// 另起一组让两者相差一个量级的数据来真正钉住加权。
	if summary.AvgLatencyMs != 898 {
		t.Fatalf("AvgLatencyMs 必须是 114 §4.3 的加权值 898，got=%d", summary.AvgLatencyMs)
	}

	// 加权 vs 算术平均的判别用例：重负载且慢的点必须主导结果。
	// 加权 = (1000*100 + 10*1) / 101 = 990；算术平均 = 505。
	weighted := SummarizeServiceStatus([]StatusPoint{
		{Minute: 1, TotalRequests: 100, SuccessRequests: 100, AvgLatencyMs: 1000},
		{Minute: 2, TotalRequests: 1, SuccessRequests: 1, AvgLatencyMs: 10},
	}, ServiceStatusWindowMinutes)
	if weighted.AvgLatencyMs != 990 {
		t.Fatalf("延迟必须按请求数加权（期望 990，算术平均会是 505），got=%d", weighted.AvgLatencyMs)
	}

	// 只给最后 5 个点：多灌两个更早的点，必须被窗口切掉。
	older := append([]StatusPoint{
		{Minute: 98, TotalRequests: 999, SuccessRequests: 999},
		{Minute: 99, TotalRequests: 999, SuccessRequests: 999},
	}, points...)
	windowed := SummarizeServiceStatus(older, ServiceStatusWindowMinutes)
	if windowed.TotalRequests != 105 {
		t.Fatalf("必须只取最后 5 个点，got total=%d", windowed.TotalRequests)
	}
}

// TestServiceStatusSamplerSealsOnNaturalMinuteBoundary 钉 114 §3.6 N4-k：
// 自然分钟边界，不是「每 60 秒滑一次」。
func TestServiceStatusSamplerSealsOnNaturalMinuteBoundary(t *testing.T) {
	base := time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)
	now := base
	var counters StatusCounters
	ring := NewServiceStatusRing()
	sampler := NewServiceStatusSampler(ring, func() time.Time { return now }, func() StatusCounters { return counters })

	// 14:00:10 二十次全成功
	counters = StatusCounters{TotalRequests: 20, LatencyCount: 20, LatencyTotalMillis: 4000}
	if got := sampler.Sample(); got.Minute != base.Unix()/60 || got.TotalRequests != 20 || got.SuccessRequests != 20 {
		t.Fatalf("第一分钟聚合错: %+v", got)
	}
	if got := sampler.Sample(); got.TotalRequests != 20 {
		t.Fatalf("同一分钟内重复采样不得重复计数（差量为 0），got=%+v", got)
	}
	// 时间推进到 14:00:59 —— 仍是同一分钟
	now = base.Add(49 * time.Second)
	if got := sampler.Sample(); got.Minute != base.Unix()/60 {
		t.Fatalf("14:00:59 仍属同一分钟，got minute=%d", got.Minute)
	}
	// 14:01:00 翻页：上一分钟必须被封存
	counters = StatusCounters{TotalRequests: 25, FailedRequests: 5, UpstreamFailures: 3, TimeoutFailures: 2,
		LatencyCount: 25, LatencyTotalMillis: 7500}
	now = base.Add(time.Minute)
	sampled := sampler.Sample()
	if sampled.Minute != now.Unix()/60 || sampled.TotalRequests != 5 {
		t.Fatalf("翻页后应只剩本分钟的差量 5，got=%+v", sampled)
	}
	if ring.Len() != 1 {
		t.Fatalf("翻页时上一分钟必须封存进环形缓冲，Len 期望 1 got=%d", ring.Len())
	}
	sealed := ring.Points()[0]
	if sealed.Minute != base.Unix()/60 || sealed.TotalRequests != 20 || sealed.SuccessRequests != 20 {
		t.Fatalf("封存的上一分钟内容错: %+v", sealed)
	}
	// 进行中的那一分钟要出现在 Points 末尾，UI 不必等下一分钟。
	points := sampler.Points()
	if len(points) != 2 || points[1].TotalRequests != 5 {
		t.Fatalf("Points 必须包含进行中的分钟，got=%+v", points)
	}
	sampler.Flush()
	if ring.Len() != 2 {
		t.Fatalf("Flush 后应封存 2 分钟，got=%d", ring.Len())
	}
}

// TestServiceStatusSamplerSkipsEmptyMinutes 钉「只封存有流量的分钟」：
// 空闲进程不能把缓冲填成 1440 个零点，否则判据 ③ 在运行期失效。
func TestServiceStatusSamplerSkipsEmptyMinutes(t *testing.T) {
	base := time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)
	now := base
	var counters StatusCounters
	ring := NewServiceStatusRing()
	sampler := NewServiceStatusSampler(ring, func() time.Time { return now }, func() StatusCounters { return counters })

	counters = StatusCounters{TotalRequests: 1}
	sampler.Sample()
	for minute := 1; minute <= 30; minute++ {
		now = base.Add(time.Duration(minute) * time.Minute)
		sampler.Sample()
	}
	if ring.Len() != 1 {
		t.Fatalf("空转的分钟不得写进缓冲，Len 期望 1 got=%d", ring.Len())
	}
	if got := ring.Points()[0]; got.TotalRequests != 1 || got.Minute != base.Unix()/60 {
		t.Fatalf("封存的必须是有流量的那一分钟，got=%+v", got)
	}
}

// TestServiceStatusSamplerResetsOnCounterRegression 钉饱和差：
// 计数回退（进程内计数器被替换、测试夹具重置）时必须取 0 差量，而不是
// 让无符号差翻成天文数字，也不是反向把回退后的值当成新流量重复计一遍。
func TestServiceStatusSamplerResetsOnCounterRegression(t *testing.T) {
	now := time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)
	counters := StatusCounters{TotalRequests: 100, LatencyCount: 100, LatencyTotalMillis: 20000}
	sampler := NewServiceStatusSampler(NewServiceStatusRing(), func() time.Time { return now },
		func() StatusCounters { return counters })
	if got := sampler.Sample(); got.TotalRequests != 100 || got.AvgLatencyMs != 200 {
		t.Fatalf("首次采样应是全量 100 / 200ms，got=%+v", got)
	}
	// 计数回退：差量饱和为 0（不知道发生了什么，不编数据）。
	counters = StatusCounters{TotalRequests: 3}
	now = now.Add(time.Minute)
	if got := sampler.Sample(); got.TotalRequests != 0 || got.FailedRequests != 0 {
		t.Fatalf("计数回退必须取 0 差量，got=%+v", got)
	}
	// 回退后基线已同步到 3，后续增量必须被正常捕获（不能因为一次回退就永久失灵）。
	counters = StatusCounters{TotalRequests: 5}
	now = now.Add(time.Minute)
	if got := sampler.Sample(); got.TotalRequests != 2 || got.SuccessRequests != 2 {
		t.Fatalf("回退后增量必须恢复正常（期望 2），got=%+v", got)
	}
}

// TestClassifyServiceStatusFailure 钉失败四分类。
func TestClassifyServiceStatusFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want StatusFailure
	}{
		{"nil 不计数", nil, StatusFailureNone},
		{"超时哨兵", uapi.ErrTimeout, StatusFailureTimeout},
		{"鉴权失败", uapi.ErrUnauthorized, StatusFailureAuth},
		{"被拒绝", uapi.ErrForbidden, StatusFailureAuth},
		{"配额不足", uapi.ErrInsufficientCredits, StatusFailureQuota},
		{"限流", uapi.ErrRateLimited, StatusFailureQuota},
		{"上游异常", uapi.ErrUpstream, StatusFailureUpstream},
		{"网络失败", uapi.ErrNetwork, StatusFailureUpstream},
		{"未分类错误落上游", errors.New("boom"), StatusFailureUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyServiceStatusFailure(tc.err); got != tc.want {
				t.Fatalf("期望 %v，got=%v", tc.want, got)
			}
		})
	}
}

// TestStatusFailureRecorderAccumulates 钉记录器是单调累计（采样器只做差）。
func TestStatusFailureRecorderAccumulates(t *testing.T) {
	recorder := &StatusFailureRecorder{}
	recorder.Observe(StatusFailureNone)
	recorder.Observe(StatusFailureTimeout)
	recorder.Observe(StatusFailureTimeout)
	recorder.Observe(StatusFailureAuth)
	recorder.Observe(StatusFailureQuota)
	recorder.Observe(StatusFailureQuota)
	recorder.Observe(StatusFailureUpstream)
	got := recorder.Snapshot()
	if got.TimeoutFailures != 2 || got.AuthFailures != 1 || got.QuotaFailures != 2 || got.UpstreamFailures != 1 {
		t.Fatalf("累计值错: %+v", got)
	}
}

// TestStartServiceStatusSamplingStopsOnClose 钉启动/停止契约。
func TestStartServiceStatusSamplingStopsOnClose(t *testing.T) {
	ring := NewServiceStatusRing()
	sampler := NewServiceStatusSampler(ring, nil, nil)
	stop := make(chan struct{})
	StartServiceStatusSampling(sampler, stop)
	StartServiceStatusSampling(nil, stop) // nil 采样器必须安全忽略
	time.Sleep(20 * time.Millisecond)
	close(stop)
	time.Sleep(20 * time.Millisecond)
	if ring.Len() != 0 {
		t.Fatalf("零流量时不应封存任何点，Len=%d", ring.Len())
	}
}
