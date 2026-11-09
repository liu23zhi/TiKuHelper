package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
)

// writeReviewRebateSSE 按 Responses API 的流式格式写一条事件。
//
// 与 internal/search 的测试辅助同构（那边是包内私有），这里自己写一份是为了
// 不把 search 的测试辅助导出到生产代码里。
func writeReviewRebateSSE(w http.ResponseWriter, event string, payload map[string]any) {
	encoded, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
}

// TestReviewRebateAgentScorerReusesSearchClientAgainstRealTransport 是本任务
// 「复用现有模型客户端」这一条的**证据性测试**：不注入假 completer，
// 而是真的构造 internal/search 的 ChatGptClient，让它对一个本地假 Responses 服务
// 发一次带图请求，再断言「端点 / 提示词 / 图片 / 解析」四件事。
//
// 它能在不联网、不配置密钥的前提下证明：
//   - 端点走 /v1/responses（仓库统一协议，不是 Chat Completions）；
//   - system prompt 用的是本功能的提示词（而不是搜题的答题提示词）；
//   - 截图以 data URL 的 input_image 形式提交；
//   - 模型输出的 JSON 能被解析成 ReviewRebateScore。
func TestReviewRebateAgentScorerReusesSearchClientAgainstRealTransport(t *testing.T) {
	t.Chdir(t.TempDir())

	var (
		gotPath        string
		gotSystem      string
		gotImageURL    string
		gotUserContent string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		// 鉴权头必须由客户端自己带上（密钥不拼进 URL）。
		if got := r.Header.Get("Authorization"); got != "Bearer sk-fixture" {
			t.Errorf("Authorization = %q", got)
		}
		var payload struct {
			Model string `json:"model"`
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "gpt-fixture" {
			t.Errorf("model = %q", payload.Model)
		}
		if len(payload.Input) != 2 {
			t.Errorf("expected system+user input, got %d items", len(payload.Input))
			return
		}
		var systemPrompt string
		if err := json.Unmarshal(payload.Input[0].Content, &systemPrompt); err != nil {
			t.Errorf("decode system prompt: %v", err)
			return
		}
		gotSystem = systemPrompt
		// user content 是 parts 数组（text + input_image）。
		var parts []map[string]any
		if err := json.Unmarshal(payload.Input[1].Content, &parts); err != nil {
			t.Errorf("decode user parts: %v", err)
			return
		}
		for _, part := range parts {
			switch part["type"] {
			case "input_text":
				if text, ok := part["text"].(string); ok {
					gotUserContent += text
				}
			case "input_image":
				if url, ok := part["image_url"].(string); ok {
					gotImageURL = url
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content := agentJSON(5, 9, 0.83, "截图清晰显示 5 星，正文写了具体使用场景。")
		writeReviewRebateSSE(w, "response.output_text.delta", map[string]any{"delta": content})
		writeReviewRebateSSE(w, "response.completed", map[string]any{
			"response": map[string]any{
				"status": "completed",
				"usage":  map[string]any{"input_tokens": 120, "output_tokens": 40, "total_tokens": 160},
			},
		})
	}))
	defer server.Close()

	db := newReviewRebateScorerTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("e1")
	screenshotPath := writeReviewRebateTestScreenshot(t, 55, digest)

	client := &internalsearch.ChatGptClient{
		Context:            context.Background(),
		Enable:             true,
		BaseURL:            server.URL + "/v1",
		APIKey:             "sk-fixture",
		Model:              "gpt-fixture",
		MaxAISearchRetries: 1,
	}
	scorer := NewReviewRebateAgentScorer(db, chatGptReviewRebateCompleter{client: client})
	score, err := scorer.Score(entity.ReviewRebateClaim{
		ID: 5, ProgramID: program.ID, UserID: 55,
		ScreenshotPath:   screenshotPath,
		ScreenshotSHA256: digest,
		ClaimedStars:     4,
		ClaimedReviewURL: "https://example.invalid/review/x",
	})
	if err != nil {
		t.Fatalf("score through the real search client: %v", err)
	}

	// 1) 端点：仓库统一用 Responses，不是 Chat Completions。
	if gotPath != "/v1/responses" {
		t.Fatalf("endpoint = %q, want /v1/responses", gotPath)
	}
	// 2) 提示词：必须是本功能的提示词，且带上活动配置。
	if !strings.Contains(gotSystem, "商店评价返现") {
		t.Fatalf("system prompt is not the review-rebate prompt: %q", gotSystem)
	}
	if strings.Contains(gotSystem, "题库检索结果补充的答题助手") {
		t.Fatal("system prompt must not be the question-answering prompt")
	}
	if !strings.Contains(gotUserContent, program.StoreURL) {
		t.Fatalf("user content must carry the store URL: %q", gotUserContent)
	}
	if !strings.Contains(gotUserContent, "未核实，不可信") {
		t.Fatalf("user content must mark the user link as untrusted: %q", gotUserContent)
	}
	// 3) 图片：以 data URL 的 input_image 提交（本地字节，不经外部 URL）。
	if !strings.HasPrefix(gotImageURL, "data:image/png;base64,") {
		t.Fatalf("image must be an inline data URL, got %q", gotImageURL)
	}
	// 4) 解析：模型输出的 JSON 被解成结构化分数。
	if score.Stars != 5 || score.ContentScore != 9 || score.Confidence != 0.83 {
		t.Fatalf("unexpected parsed score: %+v", score)
	}
	if score.Scorer != ReviewRebateScorerKindAgent {
		t.Fatalf("scorer provenance = %q, want agent", score.Scorer)
	}
}

// 上游 500 → 打分失败（不是空分数、不是默认高分）。
func TestReviewRebateAgentScorerFailsOnUpstreamError(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("e2")

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"synthetic upstream failure"}}`))
	}))
	defer server.Close()

	client := &internalsearch.ChatGptClient{
		Context:            context.Background(),
		Enable:             true,
		BaseURL:            server.URL + "/v1",
		APIKey:             "sk-fixture",
		Model:              "gpt-fixture",
		MaxAISearchRetries: 1,
	}
	scorer := NewReviewRebateAgentScorer(db, chatGptReviewRebateCompleter{client: client})
	_, err := scorer.Score(entity.ReviewRebateClaim{
		ID: 6, ProgramID: program.ID, UserID: 56,
		ScreenshotPath: writeReviewRebateTestScreenshot(t, 56, digest),
	})
	if err == nil {
		t.Fatal("upstream 500 must fail the scoring")
	}
	if !strings.Contains(err.Error(), "打分失败") && !strings.Contains(err.Error(), "模型调用失败") {
		t.Fatalf("error must be a scoring failure, got %v", err)
	}
	if attempts == 0 {
		t.Fatal("the client must actually hit the upstream")
	}
}

// 模型返回非 JSON 文本 → 打分失败。
func TestReviewRebateAgentScorerFailsOnNonJSONResponse(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newReviewRebateScorerTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("e3")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeReviewRebateSSE(w, "response.output_text.delta", map[string]any{
			"delta": "这条评价看起来不错，给满分。",
		})
		writeReviewRebateSSE(w, "response.completed", map[string]any{
			"response": map[string]any{"status": "completed", "usage": map[string]any{"total_tokens": 12}},
		})
	}))
	defer server.Close()

	client := &internalsearch.ChatGptClient{
		Context: context.Background(), Enable: true,
		BaseURL: server.URL + "/v1", APIKey: "sk-fixture", Model: "gpt-fixture",
		MaxAISearchRetries: 1,
	}
	scorer := NewReviewRebateAgentScorer(db, chatGptReviewRebateCompleter{client: client})
	_, err := scorer.Score(entity.ReviewRebateClaim{
		ID: 7, ProgramID: program.ID, UserID: 57,
		ScreenshotPath: writeReviewRebateTestScreenshot(t, 57, digest),
	})
	if err == nil {
		t.Fatal("non-JSON model output must fail the scoring, not produce a default score")
	}
}
