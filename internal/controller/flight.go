package controller

import (
	"context"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

// flightGroup 让「同一个 key 的并发请求只执行一次」，其余请求搭车复用结果。
//
// 为什么需要它：
//
//	搜题与 AI 兜底都是「一次上游调用 = 一次计费」。而结果缓存是在上游返回之后
//	才写入的，因此两个并发且 key 完全相同的请求都会读到空缓存，
//	各自打一次上游、各自计一次费。线上事故：同一用户对同一道题的两次请求
//	相隔 31 秒，第一次上游耗时 36.9 秒（客户端超时重发），两次并发重叠，
//	同一道题被计费两次（0.008940 + 0.008041）。
//
// 合并粒度由调用方决定：**必须与缓存复用条件严格一致**，
// 只有用户、题目、选项等完全一致才会得到同一个 key。
//
// 发起者的真实工作由调用方执行（需要 handler/DB 上下文，无法用闭包统一表达），
// 因此这里只提供 begin / wait / finish 三个动作。
type flightGroup[V any] struct {
	mu    sync.Mutex
	calls map[string]*flightCall[V]
}

type flightCall[V any] struct {
	done  chan struct{}
	once  sync.Once
	value V
	ok    bool
}

func newFlightGroup[V any]() *flightGroup[V] {
	return &flightGroup[V]{calls: make(map[string]*flightCall[V])}
}

// begin 尝试成为某个 key 的发起者。
//
// 返回值互斥：
//   - leader 非 nil：调用方是发起者，必须执行真实工作，完成后调用 finish 广播。
//   - shared 非 nil：已有相同请求在进行，调用方应调用 wait 复用结果，不要执行真实工作。
func (g *flightGroup[V]) begin(key string) (leader *flightCall[V], shared *flightCall[V]) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if existing, ok := g.calls[key]; ok {
		return nil, existing
	}

	leader = &flightCall[V]{done: make(chan struct{})}
	g.calls[key] = leader
	return leader, nil
}

// finish 广播结果并释放 key。
//
// ok 为 false 表示发起者没有产出可用结果，搭车者应自行继续。
//
// 幂等：内部用 once 保护，因此「正常路径带结果调用一次 + defer 兜底再调用一次」安全。
func (g *flightGroup[V]) finish(key string, call *flightCall[V], value V, ok bool) {
	if call == nil {
		return
	}

	// 先广播再释放 key：避免「key 已释放但结果还没广播」的窗口里
	// 新请求成为发起者，而老搭车者仍在等旧的结果。
	call.once.Do(func() {
		call.value = value
		call.ok = ok
		close(call.done)
	})

	g.mu.Lock()
	// 只在仍是当前 call 时删除，避免覆盖后来者的登记。
	if current, ok := g.calls[key]; ok && current == call {
		delete(g.calls, key)
	}
	g.mu.Unlock()
}

// wait 等待发起者结束并返回其结果，ok=false 表示没等到可用结果。
func (g *flightGroup[V]) wait(key string, call *flightCall[V], timeout time.Duration) (V, bool) {
	value, ok, _ := g.waitContext(context.Background(), key, call, timeout)
	return value, ok
}

// waitContext detaches only this observer; it never removes/cancels the shared
// call. A context error is distinct from an unusable result or watchdog expiry,
// so a retired observer cannot fall through into another charged execution.
func (g *flightGroup[V]) waitContext(ctx context.Context, key string, call *flightCall[V], timeout time.Duration) (V, bool, error) {
	var zero V
	if ctx == nil {
		ctx = context.Background()
	}

	if call == nil {
		return zero, false, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	if timeout <= 0 {
		timeout = defaultFlightWaitTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return zero, false, ctx.Err()
	case <-call.done:
		if err := ctx.Err(); err != nil {
			return zero, false, err
		}
		if !call.ok {
			// 发起者没拿到可用结果，搭车者自行继续。
			return zero, false, nil
		}
		return call.value, true, nil
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			return zero, false, err
		}
		logger.SysLog("等待并发相同请求的结果超时，改为自行查询")
		return zero, false, nil
	}
}

// inFlight 返回进行中的登记数（仅供测试与观测）。
func (g *flightGroup[V]) inFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// reset 清空登记（仅供测试）。
func (g *flightGroup[V]) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = make(map[string]*flightCall[V])
}

// defaultFlightWaitTimeout 是「搭车请求」等待发起者结果的上限。
//
// 取值需大于上游自身超时（AI 首字超时 5 分钟），否则会在上游本来就快返回时
// 误判为超时；同时限制住最坏等待，不会把 HTTP 连接无限拖死。
const defaultFlightWaitTimeout = 6 * time.Minute
