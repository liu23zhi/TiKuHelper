package search

import (
	"context"
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

// writeOpenAIStyleStream 以 Responses API 的 SSE 形式回写一段答案内容。
func writeOpenAIStyleStream(w http.ResponseWriter, answerJSON string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	writeAIResponsesTextStream(w, []string{answerJSON}, map[string]any{
		"input_tokens": 1000, "output_tokens": 20, "total_tokens": 1020,
		"input_tokens_details": map[string]any{"cached_tokens": 640},
	})
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
		if atomic.AddInt32(&attempts, 1) <= 2 {
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
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected two endpoint probes plus one retry probe, got %d", got)
	}
	// 必须真的退避过，而不是立刻重试打爆上游。
	if elapsed := time.Since(started); elapsed < aiSearchRetryBaseDelay {
		t.Fatalf("expected backoff before retry, elapsed %v", elapsed)
	}
}

func TestAISearchRetryAfterHeaderAndBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := &ChatGptClient{APIKey: "sk-test", Model: "test"}

	_, _, status, _, retryable, ordinaryErr := client.executeAISearchStreamRequest(context.Background(), server.URL, []byte(`{}`), `{}`)
	if status != http.StatusTooManyRequests || !retryable {
		t.Fatalf("ordinary response: status=%d retryable=%t", status, retryable)
	}
	_, toolStatus, toolErr := client.requestAIToolRound(context.Background(), server.URL, []byte(`{}`))
	if toolStatus != http.StatusTooManyRequests {
		t.Fatalf("tool response: status=%d", toolStatus)
	}
	for name, retryErr := range map[string]error{"ordinary": ordinaryErr, "tool": toolErr} {
		delay, ok := aiSearchRetryAfter(retryErr)
		if !ok || delay != time.Second {
			t.Errorf("%s Retry-After: delay=%v present=%t", name, delay, ok)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second/2)
		if sleepAISearchRetryBackoff(ctx, 1, retryErr) {
			t.Errorf("%s should not retry beyond the caller's deadline", name)
		}
		cancel()
	}
	started := time.Now()
	if !sleepAISearchRetryBackoff(context.Background(), 1, ordinaryErr) {
		t.Fatal("Retry-After within budget should be accepted")
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After was not honored: elapsed=%v", elapsed)
	}
}

func TestSearchAnswerWithUsageDoesNotReplayGatewayFailure(t *testing.T) {
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

	// A gateway failure may be reported after the model was billed.
	expectedAttempts := int32(1)
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
			{"retry recovers within one model round", 1, http.StatusTooEarly, false},
			{"bad request is not retried", 1, http.StatusBadRequest, true},
			{"no fallback after last call", 1, http.StatusNotFound, true},
			{"retry receives remaining budget", 2, http.StatusTooManyRequests, false},
		} {
			t.Run(fmt.Sprintf("%s/debug=%t", tc.name, debugMode), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != "/responses" && r.URL.Path != "/v1/responses" {
						t.Errorf("ordinary AI search must use Responses, got %s", r.URL.Path)
					}
					var payload struct {
						Messages []openAIRequestMessagePayload `json:"input"`
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
					// 普通搜索一次性直出答案：提示词里既不该有工具，也不该有预算播报。
					if strings.Contains(prompt, "当前预算") || strings.Contains(prompt, "web_search") || strings.Contains(prompt, "web_read") {
						t.Errorf("ordinary search must not announce tools or budget: %s", prompt)
					}
					if !strings.Contains(prompt, "请直接输出最终答案 JSON") {
						t.Errorf("missing direct-answer requirement: %s", prompt)
					}
					if calls == 1 || tc.status == http.StatusNotFound {
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
				if tc.status == http.StatusBadRequest {
					wantCalls = 1
				}
				if tc.status == http.StatusNotFound {
					wantCalls = 2
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
		t.Fatalf("unauthorized request must not be replayed, got %d requests", got)
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

func TestDownloadAISearchAttachmentRetriesReadOnlyFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makeTransparentFormulaImage(32, 20))
	}))
	defer server.Close()
	part, ok := downloadAISearchAttachmentPart(server.Client(), server.URL+"/image.png")
	if !ok || part.Type != "image_url" || calls.Load() != 2 {
		t.Fatalf("attachment GET should retry once: part=%+v ok=%t calls=%d", part, ok, calls.Load())
	}
}

func TestBuildAISearchPromptPartsReturnsNoAttachmentsOrReferencesWhenClientMissing(t *testing.T) {
	req := model.SearchRequest{Question: "题目 https://example.com/a.png"}
	parts := buildAISearchPromptParts(req, nil)
	if len(parts) != 1 {
		t.Fatalf("expected original prompt only without delivered images, got %d parts", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != buildAISearchPrompt(req) {
		t.Fatalf("expected unchanged prompt text only, got %+v", parts)
	}
}
