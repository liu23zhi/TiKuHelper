package uapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestImageNSFWUploadsFileAndParsesVerdict 覆盖文档给出的 200 响应形状。
func TestImageNSFWUploadsFileAndParsesVerdict(t *testing.T) {
	var gotAuth, gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"nsfw_score":0.05,"normal_score":0.95,"is_nsfw":false,"label":"normal","suggestion":"pass","risk_level":"low","confidence":0.95,"inference_time_ms":156,"extra":"kept"}`)
	}))
	defer server.Close()

	client := NewClient("uapi-test-only-key").SetImageNSFWURL(server.URL).SetMaxRetries(0)
	result, err := client.ImageNSFW(context.Background(), ImageNSFWRequest{
		Image:     []byte("\x89PNG\r\n\x1a\nfake-but-nonempty"),
		ImageName: "shot.png",
	})
	if err != nil {
		t.Fatalf("ImageNSFW: %v", err)
	}
	if gotAuth != "Bearer uapi-test-only-key" {
		t.Fatalf("key must travel in Authorization header only, got %q", gotAuth)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("expected multipart content type, got %q", gotContentType)
	}
	if !strings.Contains(gotBody, `name="file"; filename="shot.png"`) {
		t.Fatalf("expected file field with the sanitized name, body=%s", gotBody)
	}
	if result.IsNSFW == nil || *result.IsNSFW {
		t.Fatalf("expected is_nsfw=false, got %+v", result)
	}
	if result.Suggestion != "pass" || result.RiskLevel != "low" {
		t.Fatalf("unexpected verdict %+v", result)
	}
	if result.NSFWScore == nil || *result.NSFWScore != 0.05 {
		t.Fatalf("expected nsfw_score 0.05, got %+v", result.NSFWScore)
	}
	// 未知字段必须保留在 RawJSON 里，不能因为结构体没列就丢数据。
	if !strings.Contains(result.RawJSON, `"extra":"kept"`) {
		t.Fatalf("RawJSON must keep unknown fields, got %s", result.RawJSON)
	}
}

// TestImageNSFWRejectsBadInputLocally 断言本地校验在发请求之前完成（不白花钱）。
func TestImageNSFWRejectsBadInputLocally(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"is_nsfw":false}`)
	}))
	defer server.Close()
	client := NewClient("uapi-test-only-key").SetImageNSFWURL(server.URL).SetMaxRetries(0)

	cases := []struct {
		name string
		req  ImageNSFWRequest
	}{
		{name: "empty", req: ImageNSFWRequest{Image: nil, ImageName: "a.png"}},
		{name: "bad_ext", req: ImageNSFWRequest{Image: []byte("x"), ImageName: "a.txt"}},
		{name: "too_large", req: ImageNSFWRequest{Image: make([]byte, MaxImageNSFWBytes+1), ImageName: "a.png"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ImageNSFW(context.Background(), tc.req)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("local validation must not call upstream, got %d calls", calls)
	}
}

// TestImageNSFWMissingKeyDoesNotCallUpstream 没有密钥时本地失败，不发请求。
func TestImageNSFWMissingKeyDoesNotCallUpstream(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()
	client := NewClient("").SetImageNSFWURL(server.URL)
	_, err := client.ImageNSFW(context.Background(), ImageNSFWRequest{Image: []byte("x"), ImageName: "a.png"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("missing key must not call upstream, got %d calls", calls)
	}
}

// TestImageNSFWRejectsResponseWithoutVerdict 200 但没有判定字段必须报错（不假装通过）。
func TestImageNSFWRejectsResponseWithoutVerdict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"label":"normal"}`)
	}))
	defer server.Close()
	client := NewClient("uapi-test-only-key").SetImageNSFWURL(server.URL).SetMaxRetries(0)
	_, err := client.ImageNSFW(context.Background(), ImageNSFWRequest{Image: []byte("x"), ImageName: "a.png"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected ErrUpstream for verdict-less 200, got %v", err)
	}
}

// TestImageNSFWDoesNotRetryBillablePost 断言内部不重放可能计费的 POST。
func TestImageNSFWDoesNotRetryBillablePost(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	defer server.Close()
	client := NewClient("uapi-test-only-key").SetImageNSFWURL(server.URL)
	if _, err := client.ImageNSFW(context.Background(), ImageNSFWRequest{Image: []byte("x"), ImageName: "a.png"}); err == nil {
		t.Fatal("expected error on 500")
	}
	if calls != 1 {
		t.Fatalf("billable POST must not be replayed, got %d calls", calls)
	}
}
