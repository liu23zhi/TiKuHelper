package uapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestT12OwnedImageProviderAuditSafeOutcomes(t *testing.T) {
	const fakeKey = "uapi-synthetic-provider-supplement-only"
	const fakeID = "synthetic-provider-response-id-not-loggable"
	const fakeData = "synthetic-image-file-bytes-not-loggable"
	const fakePath = "synthetic-image-path-not-loggable"
	const fakeBody = "synthetic-moderation-response-body-not-loggable"
	tests := []struct {
		name          string
		httpStatus    int
		invalidImage  bool
		attempts      int
		outcome, kind string
		summary       map[string]int
	}{
		{"success_presence_only", 200, false, 1, "success", "", map[string]int{"has_is_nsfw": 1, "has_suggestion": 1, "has_risk_level": 0}},
		{"500_billable_post_never_retried", 500, false, 1, "error", "upstream_5xx", nil},
		{"invalid_local_input_zero_transport", 0, true, 0, "error", "invalid_argument", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sink bytes.Buffer
			old := gin.DefaultWriter
			gin.DefaultWriter = &sink
			defer func() { gin.DefaultWriter = old }()
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fakeKey {
					t.Error("owned fixture expected the original authenticated POST")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", fakeID)
				w.Header().Set("Set-Cookie", "synthetic_cookie="+fakeBody)
				if tt.httpStatus == 500 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = fmt.Fprintf(w, "{\"code\":\"FIXTURE_500\",\"message\":%q}", fakeBody)
					return
				}
				_, _ = fmt.Fprintf(w, "{\"is_nsfw\":false,\"nsfw_score\":0.61937,\"label\":%q,\"suggestion\":\"pass\"}", fakeBody)
			}))
			defer server.Close()
			if !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
				t.Fatal("fixture endpoint is not isolated loopback")
			}
			request := ImageNSFWRequest{Image: []byte(fakeData), ImageName: fakePath + ".png"}
			if tt.invalidImage {
				request.Image = nil
			}
			out, err := NewClient(fakeKey).SetImageNSFWURL(server.URL+"/synthetic-private-path").SetMaxRetries(4).ImageNSFW(context.Background(), request)
			switch tt.httpStatus {
			case 200:
				if err != nil || out.IsNSFW == nil || *out.IsNSFW || out.NSFWScore == nil || *out.NSFWScore != 0.61937 ||
					out.Label != fakeBody || out.RequestID != fakeID || out.HTTPStatus != 200 {
					t.Fatal("existing typed success projection has changed")
				}
			case 500:
				var api *APIError
				if !errors.As(err, &api) || api.HTTPStatus != 500 || api.RequestID != fakeID ||
					!errors.Is(err, ErrUpstream) || strings.Contains(err.Error(), fakeBody) || strings.Contains(err.Error(), fakeID) {
					t.Fatal("typed error or safe error text has changed")
				}
			default:
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatal("local validation sentinel was not preserved")
				}
			}
			if count.Load() != int32(tt.attempts) {
				t.Fatalf("wire requests changed, observed=%d expected=%d", count.Load(), tt.attempts)
			}
			for _, forbidden := range []string{fakeKey, fakeID, fakeData, fakePath, fakeBody, server.URL, "0.61937", "synthetic-private-path"} {
				if strings.Contains(sink.String(), forbidden) {
					t.Fatal("provider diagnostics logged a raw input, response value, HTTP identifier or address")
				}
			}
			var events []providerAuditEvent
			for _, line := range strings.Split(sink.String(), "\n") {
				const key = "uapi_call "
				at := strings.Index(line, key)
				if at < 0 {
					continue
				}
				var event providerAuditEvent
				if err := json.Unmarshal([]byte(line[at+len(key):]), &event); err != nil {
					t.Fatal(err)
				}
				events = append(events, event)
			}
			if len(events) != 2 {
				t.Fatalf("expected one structured start/finish pair, got %d", len(events))
			}
			a, b := events[0], events[1]
			if a.Category != "image_nsfw" || a.Category != b.Category || a.Phase != "start" || b.Phase != "finish" ||
				a.ID == "" || a.ID != b.ID || b.HTTPStatus != tt.httpStatus || b.Attempts != tt.attempts || b.Retries != 0 ||
				b.Outcome != tt.outcome || b.ErrorClass != tt.kind || !reflect.DeepEqual(b.Summary, tt.summary) ||
				(tt.attempts > 0 && len(b.UpstreamIDHash) != 64) || (tt.attempts == 0 && b.UpstreamIDHash != "") {
				t.Fatalf("unexpected audit shape for the fixture: %+v", b)
			}
			t.Logf("phase_pair=2 wire_post=%d attempts=%d retries=0 status=%d outcome=%s class=%s summary_fields=%d raw_leaks=0", count.Load(), b.Attempts, b.HTTPStatus, b.Outcome, b.ErrorClass, len(b.Summary))
		})
	}
}
