package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

func TestBuildUAPISearchToolUsesSystemSettingAndDisablesNestedRetries(t *testing.T) {
	db := newSystemSettingTestDB(t)
	if _, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{UAPIAPIKey: "uapi-test-key"}); err != nil {
		t.Fatal(err)
	}
	tool := BuildUAPISearchToolWithDB(db)
	client, ok := tool.(*uapi.Client)
	if !ok {
		t.Fatalf("expected configured UAPI search client, got %T", tool)
	}
	if _, ok := any(tool).(internalsearch.AIWebReadTool); !ok {
		t.Fatalf("configured UAPI client must expose web-read capability, got %T", tool)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL_ERROR"}`))
	}))
	defer server.Close()
	client.SetSearchAggregateURL(server.URL)
	_, err := client.SearchAggregate(context.Background(), uapi.SearchAggregateRequest{Query: "test"})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("AI tool client must make only one attempt per tool call, got %d", got)
	}
}

func TestBuildUAPISearchToolReturnsNilWhenKeyIsMissing(t *testing.T) {
	db := newSystemSettingTestDB(t)
	if got := BuildUAPISearchToolWithDB(db); got != nil {
		t.Fatalf("expected no search tool when UAPI key is missing, got %T", got)
	}
}
