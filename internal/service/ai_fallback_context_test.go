package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIFallbackContextCancellationStopsFurtherModelRequests(t *testing.T) {
	for _, canceledBeforeStart := range []bool{true, false} {
		t.Run(fmt.Sprint(canceledBeforeStart), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				cancel()
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded"}}`)
			}))
			defer server.Close()
			db := newSearchAccountConfigTestDB(t)
			_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
				Enabled: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", MaxAISearchRetries: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if canceledBeforeStart {
				cancel()
			}
			result := SearchAIFallbackWithContext(ctx, db, model.SearchRequest{Question: "all tasks", Type: 2})
			if result.Success || !strings.Contains(result.FailureDetail, context.Canceled.Error()) {
				t.Fatalf("cancellation must propagate to the private failure: %+v", result)
			}
			want := int32(1)
			if canceledBeforeStart {
				want = 0
			}
			if got := calls.Load(); got != want {
				t.Fatalf("canceled request made %d external calls, want %d", got, want)
			}
		})
	}
}
