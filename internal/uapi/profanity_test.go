package uapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckProfanitySendsBearerAndDocumentedBody(t *testing.T) {
	type captured struct {
		auth        string
		method      string
		path        string
		contentType string
		body        map[string]any
		rawURL      string
	}
	got := make(chan captured, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- captured{
			auth:        r.Header.Get("Authorization"),
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
			rawURL:      r.URL.String(),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"forbidden","original_text":"坏词","masked_text":"**","forbidden_words":["坏词"]}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetProfanityCheckURL(server.URL + "/api/v1/text/profanitycheck").SetMaxRetries(0)
	response, err := client.CheckProfanity(context.Background(), ProfanityCheckRequest{Text: "坏词"})
	if err != nil {
		t.Fatalf("CheckProfanity: %v", err)
	}

	request := <-got
	if request.method != http.MethodPost {
		t.Fatalf("method = %s，期望 POST", request.method)
	}
	if request.path != "/api/v1/text/profanitycheck" {
		t.Fatalf("path = %s", request.path)
	}
	if request.auth != "Bearer uapi-test-key" {
		t.Fatalf("authorization = %q", request.auth)
	}
	if strings.Contains(request.rawURL, "uapi-test-key") {
		t.Fatalf("密钥不得出现在 URL：%s", request.rawURL)
	}
	if !strings.HasPrefix(request.contentType, "application/json") {
		t.Fatalf("content-type = %q", request.contentType)
	}
	if request.body["text"] != "坏词" {
		t.Fatalf("body.text = %v", request.body["text"])
	}
	if len(request.body) != 1 {
		t.Fatalf("只应发送文档列出的 text 字段，实际 %v", request.body)
	}
	if !response.Forbidden() {
		t.Fatalf("status=forbidden 应判为命中：%+v", response)
	}
	if response.RawJSON == "" {
		t.Fatalf("必须保留原始 body")
	}
}

func TestCheckProfanitySkipsEmptyTextWithoutRequest(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"status":"clean"}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetProfanityCheckURL(server.URL).SetMaxRetries(0)
	if _, err := client.CheckProfanity(context.Background(), ProfanityCheckRequest{Text: "   "}); err != nil {
		t.Fatalf("空文本不该报错：%v", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("空文本不该外呼，实际 %d 次", calls)
	}
}

func TestCheckProfanityRequiresUsableKey(t *testing.T) {
	for _, key := range []string{"", "plain-key", "uapi-"} {
		client := NewClient(key)
		if _, err := client.CheckProfanity(context.Background(), ProfanityCheckRequest{Text: "x"}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("key=%q 应返回 ErrUnauthorized，实际 %v", key, err)
		}
	}
}

func TestCheckProfanityMapsHTTPErrorsToSentinels(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		expected error
	}{
		{status: http.StatusBadRequest, body: `{"code":"INVALID_ARGUMENT","message":"text 不能为空"}`, expected: ErrInvalidArgument},
		{status: http.StatusUnauthorized, body: `{"message":"unauthorized"}`, expected: ErrUnauthorized},
		{status: http.StatusPaymentRequired, body: `{"message":"no credits"}`, expected: ErrInsufficientCredits},
		{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`, expected: ErrRateLimited},
		{status: http.StatusInternalServerError, body: `{"message":"boom"}`, expected: ErrUpstream},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := NewClient("uapi-test-key").SetProfanityCheckURL(server.URL).SetMaxRetries(0)
			_, err := client.CheckProfanity(context.Background(), ProfanityCheckRequest{Text: "文本"})
			if !errors.Is(err, tc.expected) {
				t.Fatalf("status=%d 期望 %v，实际 %v", tc.status, tc.expected, err)
			}
			// 提交型 POST 不重放。
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("status=%d 应只请求 1 次，实际 %d 次（提交型 POST 不重放）", tc.status, got)
			}
		})
	}
}

// TestCheckProfanityMaps413ToTextTooLong 钉住 413 的**双哨兵**语义。
//
// 实测上游对 text 字段超过 262144 字节返回 413 {"error":"Text exceeds maximum
// length of 256KB"}。历史实现里 413 落到 ErrUpstream，导致「文本太长」与
// 「上游挂了」在监控里无法区分，超长会一直被当成偶发故障。
//
// 这里断言三件事：
//
//	① errors.Is(err, ErrTextTooLong) —— 新增的可区分入口；
//	② errors.Is(err, ErrUpstream) —— 既有判定**保持不变**（ocr/nsfw 等调用方
//	   已按 ErrUpstream 处理，不能因这个改动而改变它们的行为）；
//	③ 提交型 POST 不重放，只请求 1 次（413 重试也不会变好）。
func TestCheckProfanityMaps413ToTextTooLong(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":"Text exceeds maximum length of 256KB"}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetProfanityCheckURL(server.URL).SetMaxRetries(0)
	_, err := client.CheckProfanity(context.Background(), ProfanityCheckRequest{Text: "文本"})
	if err == nil {
		t.Fatalf("413 必须返回错误")
	}
	if !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("413 应满足 errors.Is(err, ErrTextTooLong)，实际 %v", err)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("413 必须同时满足 errors.Is(err, ErrUpstream)（保持既有调用方行为），实际 %v", err)
	}
	if !strings.Contains(err.Error(), "256KB") {
		t.Fatalf("错误信息应保留上游原文，实际 %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("应可取到 APIError 且 HTTPStatus=413，实际 %+v", apiErr)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("413 应只请求 1 次，实际 %d 次（提交型 POST 不重放）", got)
	}
}

func TestCheckProfanityHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{"status":"clean"}`))
	}))
	defer server.Close()

	client := NewClient("uapi-test-key").SetProfanityCheckURL(server.URL).SetMaxRetries(0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := client.CheckProfanity(ctx, ProfanityCheckRequest{Text: "文本"}); err == nil {
		t.Fatalf("超时必须返回错误")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("超时应在上下文截止时结束，实际耗时 %v", elapsed)
	}
}

func TestParseProfanityCheckToleratesMissingAndNullWords(t *testing.T) {
	cases := []string{
		`{"status":"clean"}`,
		`{"status":"clean","forbidden_words":null}`,
		`{"status":"clean","forbidden_words":[]}`,
		`{"status":"clean","forbidden_words":"not-an-array"}`,
	}
	for _, body := range cases {
		parsed, err := ParseProfanityCheck([]byte(body))
		if err != nil {
			t.Fatalf("body=%s 解析失败：%v", body, err)
		}
		if parsed.ForbiddenWords == nil {
			t.Fatalf("body=%s 的 ForbiddenWords 必须是空切片而不是 nil", body)
		}
		if parsed.Forbidden() {
			t.Fatalf("body=%s 不该判为命中", body)
		}
	}
}

func TestParseProfanityCheckRejectsMissingStatus(t *testing.T) {
	if _, err := ParseProfanityCheck([]byte(`{"original_text":"x"}`)); !errors.Is(err, ErrUpstream) {
		t.Fatalf("缺少 status 应报 ErrUpstream，实际 %v", err)
	}
	if _, err := ParseProfanityCheck([]byte(`not-json`)); !errors.Is(err, ErrUpstream) {
		t.Fatalf("非 JSON 应报 ErrUpstream，实际 %v", err)
	}
}
