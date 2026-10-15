package search

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIUpstreamHTTPFailurePreservesStatusAndBodyWithoutSecrets(t *testing.T) {
	const key = "sk-secret-key-for-upstream-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid API key `+key+`"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: key, Model: "test"}
	for _, debug := range []bool{false, true} {
		t.Run(fmt.Sprintf("debug=%t", debug), func(t *testing.T) {
			var err error
			if debug {
				_, _, err = client.SearchAnswerDebug(model.SearchRequest{Question: "question"})
			} else {
				_, _, err = client.SearchAnswerWithUsage(model.SearchRequest{Question: "question"})
			}
			if !errors.Is(err, pkgerrors.ErrTargetServerError) {
				t.Fatalf("expected upstream failure, got %v", err)
			}
			assertAIErrorContains(t, err, "401", "invalid API key")
			var upstream *AIUpstreamError
			if !errors.As(err, &upstream) || upstream.Stage != "http" || upstream.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected structured HTTP error, got %v", err)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("upstream error exposed API key: %v", err)
			}
		})
	}
}

func TestAIUpstreamEndpointExhaustionDoesNotBecomeNoAnswer(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "endpoint unavailable", status)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "test"}
			for _, debug := range []bool{false, true} {
				var err error
				if debug {
					_, _, err = client.SearchAnswerDebug(model.SearchRequest{Question: "question"})
				} else {
					_, _, err = client.SearchAnswerWithUsage(model.SearchRequest{Question: "question"})
				}
				if !errors.Is(err, pkgerrors.ErrTargetServerError) || errors.Is(err, pkgerrors.ErrTargetNoAnswer) {
					t.Fatalf("failed endpoints must remain upstream errors, got %v", err)
				}
				assertAIErrorContains(t, err, fmt.Sprint(status), "endpoint unavailable")
			}
		})
	}
}

func TestAIUpstreamNetworkFailurePreservesCauseWithoutURLCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	const key = "private-provider-key"
	endpoint := strings.Replace(address, "http://", "http://user:private-password@", 1) + "/v1/chat/completions?token=private-query-token"
	client := &ChatGptClient{APIKey: key}
	_, _, _, _, _, err := client.executeAISearchStreamRequest(endpoint, []byte(`{}`), `{}`, time.Now())
	if !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("expected network upstream failure, got %v", err)
	}
	assertAIErrorContains(t, err, "network", "connect")
	for _, secret := range []string{key, "private-password", "private-query-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("network error exposed %q: %v", secret, err)
		}
	}
}

func TestAIUpstreamInvalidStreamPreservesParserDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: invalid-json\n\n")
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test"}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "question"})
	assertAIErrorContains(t, err, "stream", "invalid character", "invalid-json")
}

func TestAIUpstreamStreamErrorEventRemainsServerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"error":{"message":"upstream quota exhausted"}}`+"\n\n")
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test"}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "question"})
	if !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("stream error event must remain an upstream failure, got %v", err)
	}
	assertAIErrorContains(t, err, "stream", "upstream quota exhausted")
}

func TestAIToolHTTPFailurePreservesStatusAndBodyWithoutSecrets(t *testing.T) {
	const key = "sk-secret-key-for-tool-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable, Authorization: Bearer "+key, http.StatusForbidden)
	}))
	defer server.Close()
	client := &ChatGptClient{APIKey: key}
	_, _, err := client.requestAIToolRound(context.Background(), server.URL, []byte(`{}`))
	if !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("expected upstream failure, got %v", err)
	}
	assertAIErrorContains(t, err, "403", "model unavailable")
	if strings.Contains(err.Error(), key) {
		t.Fatalf("tool error exposed API key: %v", err)
	}
}

func TestAIToolInvalidStreamPreservesPartialRawResponse(t *testing.T) {
	const key = "sk-secret-key-for-tool-stream"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: invalid-json "+key+"\n\n")
	}))
	defer server.Close()
	client := &ChatGptClient{APIKey: key}
	result, _, err := client.requestAIToolRound(context.Background(), server.URL, []byte(`{}`))
	assertAIErrorContains(t, err, "tool_stream", "invalid-json")
	if !strings.Contains(result.Raw, "invalid-json") || strings.Contains(err.Error(), key) {
		t.Fatalf("missing raw diagnostics or exposed key: raw=%q error=%v", result.Raw, err)
	}
}

func TestAIUpstreamErrorDetailsAreBoundedAndRedacted(t *testing.T) {
	client := &ChatGptClient{APIKey: "custom-provider-key", BaseURL: "https://username:password-in-url@example.com/v1?api_key=query-secret"}
	body := "custom-provider-key https://username:password-in-url@example.com/v1?api_key=query-secret Authorization: Bearer generic-bearer-key sk-other-provider-secret uapi-search-provider-secret " + strings.Repeat("题", 2000)
	err := client.upstreamError("http", http.StatusBadRequest, body, context.DeadlineExceeded)
	var upstream *AIUpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("missing structured upstream error: %v", err)
	}
	if len(upstream.Detail) > aiUpstreamErrorMaxDetailBytes+3 || !utf8.ValidString(upstream.Detail) {
		t.Fatalf("error details must be bounded valid UTF-8: %d bytes", len(upstream.Detail))
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("expected both original cause and server classification: %v", err)
	}
	for _, secret := range []string{client.APIKey, "username", "password-in-url", "query-secret", "generic-bearer-key", "sk-other-provider-secret", "uapi-search-provider-secret"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(upstream.Detail, secret) {
			t.Fatalf("upstream detail exposed %q: %v", secret, err)
		}
	}
}

func TestAIUpstreamErrorRedactsTrimmedConfiguredKey(t *testing.T) {
	client := &ChatGptClient{APIKey: "  custom-key-without-standard-prefix  "}
	err := client.upstreamError("http", http.StatusUnauthorized, "rejected key custom-key-without-standard-prefix", nil)
	if strings.Contains(err.Error(), strings.TrimSpace(client.APIKey)) {
		t.Fatalf("trimmed configured key exposed: %v", err)
	}
}

func TestAIUpstreamErrorPreservesNetworkTimeoutClassification(t *testing.T) {
	cause := &net.DNSError{Err: "i/o timeout", Name: "example.com", IsTimeout: true}
	err := (&ChatGptClient{}).upstreamError("network", 0, "", fmt.Errorf("dial failed: %w", cause))
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("test must cover a network timeout independent of the context deadline")
	}
	var networkErr net.Error
	if !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("expected underlying network timeout to remain classifiable, got %v", err)
	}
	if !errors.Is(err, pkgerrors.ErrTargetServerError) || errors.Unwrap(err) != pkgerrors.ErrTargetServerError {
		t.Fatalf("expected existing sentinel and public unwrap behavior to remain intact, got %v", err)
	}
}

func TestAIEmptySuccessfulResponseRemainsNoAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOpenAIStyleStream(w, `{"answers":[]}`)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "test"}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "question"})
	if !errors.Is(err, pkgerrors.ErrTargetNoAnswer) {
		t.Fatalf("valid empty response should remain no-answer, got %v", err)
	}
}

func TestAINormalSearchRetriesWithinOneModelRound(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary upstream failure", http.StatusBadGateway)
			return
		}
		writeOpenAIStyleStream(w, `{"answers":["4"]}`)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", MaxAISearchModelCalls: 1, MaxAISearchRetries: 2}
	answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?"})
	if err != nil || calls != 2 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("normal search should retry within one round: answer=%v calls=%d err=%v", answer, calls, err)
	}
}

func assertAIErrorContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("expected error to contain %q, got %v", part, err)
		}
	}
}
