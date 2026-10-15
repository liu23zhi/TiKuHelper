package service

import (
	"context"
	"sync"
	"time"
)

// locationFlightResult 是一次归属地查询的结果。
type locationFlightResult struct {
	address string
	rawJSON string
	ok      bool
	err     error
	// abort 表示这次失败属于「密钥/权限/配额」类问题 —— 它对**同一个密钥的所有**
	// 请求都会失败，因此没有理由再查后面的数据。与「这个号码查不到」（404）区分开：
	// 后者只是单条数据的问题，不该让整页放弃。
	abort bool
}

// locationFlightGroup 合并「同一 key 的并发归属地查询」。
//
// 为什么需要：用户管理一页可能有几十个手机号/IP，翻页或多人同时打开会产生
// 大量重复查询；而每次 UAPI 查询都是**真实计费**的。合并后同一 key 只查一次。
//
// 与搜题/AI 兜底一样，这是**独立实例**，不与其他链路共用状态 —— 不同用途的
// 并发合并不应互相干扰。
type locationFlightGroup struct {
	mu    sync.Mutex
	calls map[string]*locationFlightCall
}

type locationFlightCall struct {
	done   chan struct{}
	once   sync.Once
	result locationFlightResult
}

func newLocationFlightGroup() *locationFlightGroup {
	return &locationFlightGroup{calls: make(map[string]*locationFlightCall)}
}

// locationFlightWaitTimeout 是搭车者等待发起者结果的上限。
//
// 取值大于单次查询超时（locationLookupTimeout），避免在上游本来就快返回时误判超时。
const locationFlightWaitTimeout = locationLookupMaxAttempts*locationLookupTimeout + 5*time.Second

// do 执行 lookup，除非已有相同 key 的查询在进行 —— 此时复用其结果。
func (g *locationFlightGroup) do(key string, lookup func() locationFlightResult) locationFlightResult {
	g.mu.Lock()
	if existing, ok := g.calls[key]; ok {
		g.mu.Unlock()
		return g.wait(existing)
	}
	current := &locationFlightCall{done: make(chan struct{})}
	g.calls[key] = current
	g.mu.Unlock()

	result := locationFlightResult{}
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		current.once.Do(func() {
			current.result = result
			close(current.done)
		})
		if registered, ok := g.calls[key]; ok && registered == current {
			delete(g.calls, key)
		}
	}()
	result = lookup()
	return result
}

func (g *locationFlightGroup) wait(call *locationFlightCall) locationFlightResult {
	timer := time.NewTimer(locationFlightWaitTimeout)
	defer timer.Stop()

	select {
	case <-call.done:
		return call.result
	case <-timer.C:
		// 等不到就放弃本次复用，由调用方按「未取到」处理，
		// 避免把请求挂死（界面上这一列退化为只显示原始值）。
		return locationFlightResult{err: context.DeadlineExceeded}
	}
}

// reset 清空登记（仅供测试）。
func (g *locationFlightGroup) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = make(map[string]*locationFlightCall)
}
