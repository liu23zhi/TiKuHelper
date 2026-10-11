package controller

import (
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// 兜底合并的 key 必须「完全一致才相同」：题目/类型/选项/用户任一不同都要区分开。
func TestBuildFallbackFlightKeyRequiresExactMatch(t *testing.T) {
	base := model.SearchRequest{Question: "同一道题", Type: 2, Options: []string{"A", "B"}}

	same := buildFallbackFlightKey(7, base)
	if same != buildFallbackFlightKey(7, base) {
		t.Fatal("expected identical requests to produce the same key")
	}

	others := map[string]model.SearchRequest{
		"不同用户":   {Question: "同一道题", Type: 2, Options: []string{"A", "B"}},
		"不同题目":   {Question: "另一道题", Type: 2, Options: []string{"A", "B"}},
		"不同类型":   {Question: "同一道题", Type: 0, Options: []string{"A", "B"}},
		"不同选项":   {Question: "同一道题", Type: 2, Options: []string{"A", "C"}},
		"选项顺序不同": {Question: "同一道题", Type: 2, Options: []string{"B", "A"}},
		"选项多一条":  {Question: "同一道题", Type: 2, Options: []string{"A", "B", "C"}},
		"无选项":    {Question: "同一道题", Type: 2},
	}
	for name, req := range others {
		userID := int32(7)
		if name == "不同用户" {
			userID = 8
		}
		if buildFallbackFlightKey(userID, req) == same {
			t.Fatalf("expected %s to produce a different key", name)
		}
	}
}

// 回归：并发且完全一致的兜底请求只能有一次上游调用与一次计费。
//
// 兜底同样是「一次上游调用 = 一次计费」，缓存写入又在上游返回之后，
// 因此并发提交相同题目会各自调用 AI、各自扣费。
func TestConcurrentIdenticalFallbackSharesOneUpstreamCallAndOneBilling(t *testing.T) {
	fallbackFlights.reset()
	defer fallbackFlights.reset()

	var mu sync.Mutex
	upstreamCalls := 0
	billingCalls := 0
	release := make(chan struct{})

	// 模拟任务体：发起者执行上游 + 计费，搭车者复用且不计费。
	runTask := func() service.AIFallbackResult {
		flightKey := buildFallbackFlightKey(7, model.SearchRequest{Question: "并发同一道题", Type: 2})
		isLeader, borrowed, leader := beginFallbackFlight(flightKey)

		if !isLeader && borrowed != nil {
			return *borrowed
		}
		if isLeader {
			defer finishFallbackFlight(flightKey, leader, nil)
		}

		mu.Lock()
		upstreamCalls++
		mu.Unlock()
		if isLeader {
			// 卡住发起者，保证搭车者在此期间进入。
			<-release
		}

		result := service.AIFallbackResult{
			Success: true,
			Result:  &service.AIFallbackAnswer{Question: "并发同一道题", Answer: "合并后的答案"},
			Model:   "gpt-6.1-sol",
		}

		// 只有真正调用上游的发起者才计费。
		mu.Lock()
		billingCalls++
		mu.Unlock()

		if isLeader {
			finishFallbackFlight(flightKey, leader, &result)
		}
		return result
	}

	const concurrency = 3
	results := make([]service.AIFallbackResult, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = runTask()
		}(i)
	}

	// 等发起者登记完成，再放行。
	waitForFallbackFlight(t, 1)
	// 给搭车者时间进入等待。
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	calls := upstreamCalls
	bills := billingCalls
	mu.Unlock()

	if calls != 1 {
		t.Fatalf("expected exactly one upstream call for identical concurrent fallbacks, got %d", calls)
	}
	if bills != 1 {
		t.Fatalf("expected exactly one billing call, got %d", bills)
	}
	for i, res := range results {
		if !res.Success || res.Result == nil || res.Result.Answer != "合并后的答案" {
			t.Fatalf("expected every caller to receive the shared answer, results[%d]=%+v", i, res)
		}
	}
}

// 发起者失败时不能让搭车者一直挂着：key 必须被释放。
func TestFallbackFlightReleasedWhenLeaderFails(t *testing.T) {
	fallbackFlights.reset()
	defer fallbackFlights.reset()

	flightKey := buildFallbackFlightKey(7, model.SearchRequest{Question: "失败题", Type: 2})

	// 发起者拿到失败结果并广播。
	isLeader, borrowed, leader := beginFallbackFlight(flightKey)
	if !isLeader || leader == nil || borrowed != nil {
		t.Fatalf("expected the first caller to become leader, got isLeader=%t", isLeader)
	}
	failed := service.BuildAIFallbackUnavailableResult()
	finishFallbackFlight(flightKey, leader, &failed)

	if pending := fallbackFlights.inFlight(); pending != 0 {
		t.Fatalf("expected the flight to be released, still %d pending", pending)
	}

	// 后续请求应当立刻成为新的发起者，而不是等到超时。
	isLeader, _, leader = beginFallbackFlight(flightKey)
	if !isLeader || leader == nil {
		t.Fatal("expected the next caller to become leader after release")
	}
	finishFallbackFlight(flightKey, leader, nil)
}

// 搭车者必须拿到与发起者一致的结果，且不能重复计费。
func TestFallbackFollowerReusesLeaderResultWithoutBilling(t *testing.T) {
	fallbackFlights.reset()
	defer fallbackFlights.reset()

	flightKey := buildFallbackFlightKey(9, model.SearchRequest{Question: "共享题", Type: 2})

	isLeader, _, leader := beginFallbackFlight(flightKey)
	if !isLeader {
		t.Fatal("expected the first caller to become leader")
	}

	type outcome struct {
		borrowed *service.AIFallbackResult
		ok       bool
	}
	followerDone := make(chan outcome, 1)
	go func() {
		isLeaderFollower, borrowed, _ := beginFallbackFlight(flightKey)
		followerDone <- outcome{borrowed: borrowed, ok: !isLeaderFollower && borrowed != nil}
	}()

	// 等搭车者登记等待，再广播结果。
	time.Sleep(80 * time.Millisecond)
	shared := service.AIFallbackResult{
		Success: true,
		Result:  &service.AIFallbackAnswer{Question: "共享题", Answer: "发起者答案"},
		Model:   "gpt-6.1-sol",
	}
	finishFallbackFlight(flightKey, leader, &shared)

	select {
	case got := <-followerDone:
		if !got.ok {
			t.Fatalf("expected the follower to reuse the leader result, got %+v", got.borrowed)
		}
		if got.borrowed.Result == nil || got.borrowed.Result.Answer != "发起者答案" {
			t.Fatalf("expected the shared answer, got %+v", got.borrowed.Result)
		}
		if got.borrowed.Model != "gpt-6.1-sol" {
			t.Fatalf("expected the shared model, got %q", got.borrowed.Model)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follower did not receive the leader result")
	}
}

// waitForFallbackFlight 等待至少有 want 个兜底 key 处于「进行中」。
func waitForFallbackFlight(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fallbackFlights.inFlight() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d in-flight fallback flight(s)", want)
}
