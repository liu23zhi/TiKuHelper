package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// writeOpenAIStyleStream 以 OpenAI 兼容的 SSE 形式回写一段答案内容。
func writeOpenAIStyleStream(w http.ResponseWriter, answerJSON string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	chunks := []string{answerJSON}
	for _, chunk := range chunks {
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": chunk}}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	usage, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     1000,
			"completion_tokens": 20,
			"total_tokens":      1020,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": 640,
			},
		},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", usage)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func TestSearchAnswerWithUsageParsesCachedInputTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOpenAIStyleStream(w, `{"answers":["B"]}`)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		BaseURL: server.URL,
		APIKey:  "sk-test",
		Model:   "gpt-test",
	}

	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "B" {
		t.Fatalf("unexpected answers: %v", answers)
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != 20 || usage.TotalTokens != 1020 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if usage.CachedInputTokens != 640 {
		t.Fatalf("expected cached input tokens 640, got %d", usage.CachedInputTokens)
	}
}

func TestSearchAnswerWithUsageRetriesRateLimitThenSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		writeOpenAIStyleStream(w, `{"answers":["A"]}`)
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test"}

	started := time.Now()
	answers, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"})
	if err != nil {
		t.Fatalf("expected retry to recover from 429, got %v", err)
	}
	if len(answers) != 1 || answers[0][0] != "A" {
		t.Fatalf("unexpected answers after retry: %v", answers)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected exactly 2 upstream attempts, got %d", got)
	}
	// 必须真的退避过，而不是立刻重试打爆上游。
	if elapsed := time.Since(started); elapsed < aiSearchRetryBaseDelay {
		t.Fatalf("expected backoff before retry, elapsed %v", elapsed)
	}
}

func TestSearchAnswerWithUsageStopsRetryingAfterMaxAttemptsPerEndpoint(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, `{"error":"bad gateway"}`, http.StatusBadGateway)
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test"}

	if _, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"}); err == nil {
		t.Fatal("expected persistent 502 to fail")
	}

	// 每个候选端点都是一个逻辑模型轮次，重试机会不消耗轮次预算。
	expectedAttempts := int32(aiSearchRetryMaxAttempts * 2)
	if got := atomic.LoadInt32(&attempts); got != expectedAttempts {
		t.Fatalf("expected %d attempts, got %d", expectedAttempts, got)
	}
}

func TestOrdinaryAISearchHonorsModelBudgetAcrossRetriesAndEndpoints(t *testing.T) {
	for _, debugMode := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			budget  int
			status  int
			wantErr bool
		}{
			{"retry recovers within one model round", 1, http.StatusBadGateway, false},
			{"no fallback after last call", 1, http.StatusNotFound, true},
			{"retry receives remaining budget", 2, http.StatusTooManyRequests, false},
		} {
			t.Run(fmt.Sprintf("%s/debug=%t", tc.name, debugMode), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var payload struct {
						Messages []openAIRequestMessagePayload `json:"messages"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode request: %v", err)
						return
					}
					var prompt string
					if err := json.Unmarshal(payload.Messages[0].Content, &prompt); err != nil {
						t.Errorf("decode prompt: %v", err)
						return
					}
					logicalCall := 1 // retries stay within the first logical model round
					remaining := fmt.Sprintf("还剩模型调用 %d 次", tc.budget-logicalCall)
					if !strings.Contains(prompt, remaining) || !strings.Contains(prompt, "web_search 搜索还剩 0 次") {
						t.Errorf("missing current budget %q: %s", remaining, prompt)
					}
					if logicalCall == tc.budget && !strings.Contains(prompt, "最后一次模型调用") {
						t.Errorf("missing final answer requirement: %s", prompt)
					}
					if calls == 1 {
						http.Error(w, "temporary failure", tc.status)
						return
					}
					writeOpenAIStyleStream(w, `{"answers":["4"]}`)
				}))
				defer server.Close()
				client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test", MaxAISearchModelCalls: tc.budget}
				var err error
				if debugMode {
					_, _, err = client.SearchAnswerDebug(model.SearchRequest{Question: "2+2=?"})
				} else {
					_, _, err = client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?"})
				}
				wantCalls := 2
				if tc.status == http.StatusNotFound {
					wantCalls = 1
				}
				if (err != nil) != tc.wantErr || calls != wantCalls {
					t.Fatalf("calls=%d want=%d err=%v wantErr=%t", calls, wantCalls, err, tc.wantErr)
				}
			})
		}
	}
}

func TestSearchAnswerWithUsageDoesNotRetryClientErrors(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test"}

	if _, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"}); err == nil {
		t.Fatal("expected 401 to fail")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected 401 to fail fast without retry, got %d attempts", got)
	}
}

func TestSearchAnswerWithUsageTreatsUncertainAsParsedButUnusableAnswer(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writeOpenAIStyleStream(w, `{"answers":["不确定"]}`)
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test"}

	answers, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"})
	if err != nil {
		t.Fatalf("expected uncertain answer to still parse, got %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "不确定" {
		t.Fatalf("expected parsed uncertain answer, got %v", answers)
	}

	// 客户端只做解析；把「不确定」判定为无答案是 service 层的职责，
	// 但要保证每个候选端点只请求一次，不会在同一端点上反复重试。
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected single attempt per endpoint, got %d", got)
	}
}

func TestAISearchEndpointFallbackStatusClassification(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusNotFound,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if !isAISearchEndpointFallbackStatus(statusCode) {
			t.Fatalf("expected status %d to trigger endpoint fallback", statusCode)
		}
	}

	for _, statusCode := range []int{
		http.StatusOK,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
	} {
		if isAISearchEndpointFallbackStatus(statusCode) {
			t.Fatalf("expected status %d to fail fast without fallback", statusCode)
		}
	}
}

func TestAISearchRequestSemaphoreBoundsConcurrentUpstreamCalls(t *testing.T) {
	var inFlight int32
	var peak int32

	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&peak)
			if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		writeOpenAIStyleStream(w, `{"answers":["A"]}`)
	}))
	defer server.Close()

	// 用比默认上限更小的信号量验证限流确实生效。
	originalSemaphore := aiSearchRequestSemaphore
	limited := newAISearchSemaphore(3)
	aiSearchRequestSemaphore = limited
	defer func() { aiSearchRequestSemaphore = originalSemaphore }()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-test"}

	const callers = 12
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"})
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if observed := atomic.LoadInt32(&peak); observed > 3 {
		t.Fatalf("expected at most 3 concurrent upstream calls, observed %d", observed)
	}
	if observed := atomic.LoadInt32(&peak); observed == 0 {
		t.Fatal("expected the test to observe concurrent traffic")
	}
}

func TestDownloadAISearchAttachmentPartsPreservesOrderAndReportsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first.png":
			// 第一张图明显更宽；稍作延迟以验证并发下载不会打乱结果顺序。
			time.Sleep(40 * time.Millisecond)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(makeTransparentFormulaImage(60, 24))
		case "/second.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(makeTransparentFormulaImage(40, 22))
		default:
			http.Error(w, "gone", http.StatusNotFound)
		}
	}))
	defer server.Close()

	parts := downloadAISearchAttachmentParts(server.Client(), []string{
		server.URL + "/first.png",
		server.URL + "/missing.png",
		server.URL + "/second.png",
	})

	// 中间那张图下载失败，必须被跳过而不是占位或让整题失败。
	if len(parts) != 2 {
		t.Fatalf("expected 2 downloaded images, got %d", len(parts))
	}
	for _, part := range parts {
		if part.Type != "image_url" || part.ImageURL == nil {
			t.Fatalf("expected image parts, got %+v", part)
		}
	}

	firstImage := decodeTestPNG(t, decodeDataURLBytes(t, parts[0].ImageURL.URL))
	secondImage := decodeTestPNG(t, decodeDataURLBytes(t, parts[1].ImageURL.URL))

	// 图片按出现顺序编号，顺序错乱会让「图一/图二为」的对应关系失效。
	if firstImage.Bounds().Dx() <= secondImage.Bounds().Dx() {
		t.Fatalf("expected the wider first image to stay first, got %v then %v", firstImage.Bounds(), secondImage.Bounds())
	}
}

func TestDownloadAISearchAttachmentPartsReturnsNilWithoutClientOrURLs(t *testing.T) {
	if parts := downloadAISearchAttachmentParts(nil, []string{"https://example.com/a.png"}); parts != nil {
		t.Fatalf("expected nil parts without a client, got %+v", parts)
	}
	if parts := downloadAISearchAttachmentParts(http.DefaultClient, nil); parts != nil {
		t.Fatalf("expected nil parts without urls, got %+v", parts)
	}
}

func TestBuildAISearchPromptPartsReturnsNoAttachmentsWhenClientMissing(t *testing.T) {
	parts := buildAISearchPromptParts(model.SearchRequest{Question: "题目 https://example.com/a.png"}, nil)
	if len(parts) != 2 {
		t.Fatalf("expected text plus reference text, got %d parts", len(parts))
	}
	if parts[0].Type != "text" || parts[1].Type != "text" {
		t.Fatalf("expected text-only parts, got %+v", parts)
	}
}
