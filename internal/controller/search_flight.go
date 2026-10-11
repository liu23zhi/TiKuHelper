package controller

import (
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

// defaultSearchFlightWaitTimeout 是「搭车请求」等待发起者结果的上限。
//
// 上游本身有 first-token 超时，正常不会有一个请求无限挂住；
// 这里再兜一层，保证即使发起者卡死，搭车者最终也会自己继续，
// 不会把整个 HTTP 连接拖死。
const defaultSearchFlightWaitTimeout = 6 * time.Minute

// searchFlightResult 是一次「合并进行中的相同请求」的执行结果。
type searchFlightResult struct {
	answers [][]string
	// shared 为 true 表示复用了他人的结果（未产生上游调用，因此不应计费）。
	shared bool
}

// searchFlightGroup 让「同一用户 + 完全相同的题目与选项」的并发请求只执行一次。
//
// 为什么需要它：
//
//	搜题结果缓存是在上游返回之后才写入的，因此两个并发且完全相同的请求
//	都会在缓存为空时读取 —— 于是各自打一次上游、各自计一次费。
//	线上事故：同一用户对同一道题的两次请求相隔 31 秒，
//	第一次上游耗时 36.9 秒（客户端超时后重发），两次请求并发重叠，
//	于是同一道题被计费两次（0.008940 + 0.008041）。
//
// 合并的粒度是「缓存 key」：只有题目、选项、用户、缓存代次完全一致才会合并，
// 与缓存复用条件保持严格一致 —— 任何一个字段不同都不会搭车。
type searchFlightGroup struct {
	mu    sync.Mutex
	calls map[string]*searchFlightCall
}

type searchFlightCall struct {
	done    chan struct{}
	once    sync.Once
	answers [][]string
}

var defaultSearchFlights = &searchFlightGroup{calls: make(map[string]*searchFlightCall)}

// begin 尝试成为某个 key 的发起者。
//
// 返回值：
//   - call 非 nil：调用方是发起者，必须执行 work，然后调用 call.finish(answers)。
//   - shared 非 nil：已经有相同请求在进行，调用方应调用 shared.wait(timeout) 复用结果，不要执行 work。
//
// 两者互斥，且必然有一个非 nil。
func (g *searchFlightGroup) begin(key string) (call *searchFlightCall, shared *searchFlightCall) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if existing, ok := g.calls[key]; ok {
		// 已有相同请求在进行：搭车。
		return nil, existing
	}

	call = &searchFlightCall{done: make(chan struct{})}
	g.calls[key] = call
	return call, nil
}

// finish 结束一次发起，把结果广播给所有搭车者并释放 key。
func (g *searchFlightGroup) finish(key string, call *searchFlightCall, answers [][]string) {
	if call == nil {
		return
	}

	g.mu.Lock()
	// 只在仍是当前 call 时删除，避免覆盖后来者的登记。
	if current, ok := g.calls[key]; ok && current == call {
		delete(g.calls, key)
	}
	g.mu.Unlock()

	call.once.Do(func() {
		call.answers = answers
		close(call.done)
	})
}

// wait 等待发起者结束并返回其结果。
//
// 返回 shared=false 表示没等到可用结果（超时，或发起者没有产出答案），
// 调用方应当自行继续执行，避免把请求挂死。
func (g *searchFlightGroup) wait(key string, call *searchFlightCall, timeout time.Duration) ([][]string, bool) {
	if call == nil {
		return nil, false
	}
	if timeout <= 0 {
		timeout = defaultSearchFlightWaitTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-call.done:
		if !hasUsableSearchAnswers(call.answers) {
			// 发起者没拿到答案，搭车者自己再试一次。
			return nil, false
		}
		return cloneSearchAnswers(call.answers), true
	case <-timer.C:
		logger.SysLog("等待并发相同请求的结果超时，改为自行查询")
		return nil, false
	}
}
