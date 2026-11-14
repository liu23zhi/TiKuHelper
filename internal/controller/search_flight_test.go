package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// resetSearchFlights 清空进行中的并发合并登记，避免测试之间互相影响。
func resetSearchFlights() {
	searchFlights.reset()
}

// searchOnce 发起一次搜题请求并返回响应（可在 goroutine 中安全使用）。
func searchOnce(target string, body string, authorization string) (model.SearchResponse, int) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	ctx.Request = request

	Search(ctx)

	var response model.SearchResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return response, recorder.Code
}

// 回归：并发且**完全一致**的请求只能打一次上游、只能计一次费。
//
// 线上事故：缓存是在上游返回之后才写入的，所以两个并发相同请求都会读到空缓存，
// 各自打一次上游、各自计一次费。日志中同一用户对同一道题的两次请求相隔 31 秒，
// 第一次上游耗时 36.9 秒（客户端超时重发），两次并发重叠，
// 同一道题被计费两次（0.008940 + 0.008041）。
func TestConcurrentIdenticalSearchesShareOneUpstreamCallAndOneBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetSearchFlights()
	defer resetSearchFlights()

	var mu sync.Mutex
	upstreamCalls := 0
	billingReports := 0
	// release 由测试控制上游何时返回，确保两个请求真正重叠。
	release := make(chan struct{})

	originalReporter := searchBillingReporter
	defer func() { searchBillingReporter = originalReporter }()
	searchBillingReporter = func(searchBillingContext) {
		mu.Lock()
		billingReports++
		mu.Unlock()
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
				UserID:     201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					mu.Lock()
					upstreamCalls++
					mu.Unlock()
					// 卡住上游，让第二个请求在此期间进入。
					<-release
					return [][]string{{"A"}}, nil
				},
			}
		},
		nil,
		func() {
			const concurrency = 2
			responses := make([]model.SearchResponse, concurrency)
			statuses := make([]int, concurrency)
			var wg sync.WaitGroup

			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					responses[index], statuses[index] = searchOnce(
						"/api/search?use=icodef&noRecord=1",
						`{"question":"并发同一道题","options":["A","B"]}`,
						"Basic token-a",
					)
				}(i)
			}

			// 等发起者登记完成（说明关键路径已进入合并逻辑），再放行上游。
			waitForFlightRegistration(t, 1)
			close(release)
			wg.Wait()

			mu.Lock()
			calls := upstreamCalls
			reports := billingReports
			mu.Unlock()

			if calls != 1 {
				t.Fatalf("expected exactly one upstream call for identical concurrent requests, got %d", calls)
			}
			if reports != 1 {
				t.Fatalf("expected exactly one billing report, got %d", reports)
			}
			// 每个调用方都必须拿到同一份答案（内容会被 FillAnswerResponse 归一化，故只比较彼此一致且非空）。
			first := responses[0].Answer.AnswerText
			if first == "" {
				t.Fatal("expected a non-empty answer")
			}
			for i, resp := range responses {
				if statuses[i] != http.StatusOK {
					t.Fatalf("expected status 200 for response[%d], got %d", i, statuses[i])
				}
				if resp.Answer.AnswerText != first {
					t.Fatalf("expected every caller to receive the same answer, got response[0]=%q response[%d]=%q", first, i, resp.Answer.AnswerText)
				}
			}
		})
}

// 并发合并不改变「不同问题不共用」的语义。
func TestConcurrentDifferentQuestionsDoNotShareUpstreamCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetSearchFlights()
	defer resetSearchFlights()

	var mu sync.Mutex
	upstreamCalls := 0

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
				UserID:     201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					mu.Lock()
					upstreamCalls++
					mu.Unlock()
					return [][]string{{"答案"}}, nil
				},
			}
		},
		nil,
		func() {
			var wg sync.WaitGroup
			bodies := []string{
				`{"question":"问题一","options":["A","B"]}`,
				`{"question":"问题二","options":["A","B"]}`,
			}
			for _, body := range bodies {
				wg.Add(1)
				go func(payload string) {
					defer wg.Done()
					searchOnce("/api/search?use=icodef&noRecord=1", payload, "Basic token-a")
				}(body)
			}
			wg.Wait()

			mu.Lock()
			calls := upstreamCalls
			mu.Unlock()

			if calls != len(bodies) {
				t.Fatalf("expected %d upstream calls for different questions, got %d", len(bodies), calls)
			}
		})
}

// 选项不同（即使问题相同）不得合并 —— 与「完全一样才复用」的缓存策略保持一致。
func TestConcurrentSameQuestionDifferentOptionsDoNotShareUpstreamCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetSearchFlights()
	defer resetSearchFlights()

	var mu sync.Mutex
	upstreamCalls := 0

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
				UserID:     201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					mu.Lock()
					upstreamCalls++
					mu.Unlock()
					return [][]string{{"答案"}}, nil
				},
			}
		},
		nil,
		func() {
			var wg sync.WaitGroup
			bodies := []string{
				`{"question":"同一道题","options":["A","B"]}`,
				`{"question":"同一道题","options":["A","C"]}`,
			}
			for _, body := range bodies {
				wg.Add(1)
				go func(payload string) {
					defer wg.Done()
					searchOnce("/api/search?use=icodef&noRecord=1", payload, "Basic token-a")
				}(body)
			}
			wg.Wait()

			mu.Lock()
			calls := upstreamCalls
			mu.Unlock()

			if calls != len(bodies) {
				t.Fatalf("expected %d upstream calls for different options, got %d", len(bodies), calls)
			}
		})
}

// waitForFlightRegistration 等待至少有 want 个 key 处于「进行中」。
func waitForFlightRegistration(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		count := searchFlights.inFlight()
		if count >= want {
			// 登记完成后，再给搭车者一点时间去命中同一个 key。
			time.Sleep(30 * time.Millisecond)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d in-flight search flight(s)", want)
}

// 发起者失败时不能让搭车者一直挂着：key 必须被释放，后续请求能立即重新发起。
func TestSearchFlightReleasedWhenLeaderFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetSearchFlights()
	defer resetSearchFlights()

	var mu sync.Mutex
	upstreamCalls := 0
	fail := true

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
				UserID:     201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					mu.Lock()
					upstreamCalls++
					shouldFail := fail
					mu.Unlock()
					if shouldFail {
						return nil, errors.New("上游失败")
					}
					return [][]string{{"A"}}, nil
				},
			}
		},
		nil,
		func() {
			// 第一次：上游失败，不应留下永不释放的 in-flight 登记。
			searchOnce("/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")

			pending := searchFlights.inFlight()
			if pending != 0 {
				t.Fatalf("expected the flight to be released after a failed leader, still %d pending", pending)
			}

			// 第二次：应当立刻重新发起，而不是等到超时。
			mu.Lock()
			fail = false
			mu.Unlock()

			done := make(chan model.SearchResponse, 1)
			go func() {
				resp, _ := searchOnce("/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
				done <- resp
			}()

			select {
			case resp := <-done:
				if resp.Answer.AnswerText == "" {
					t.Fatal("expected a recovered answer after the leader failure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("second request hung：flight key was not released")
			}

			mu.Lock()
			calls := upstreamCalls
			mu.Unlock()
			if calls != 2 {
				t.Fatalf("expected the retry to call upstream again, got %d calls", calls)
			}
		})
}
