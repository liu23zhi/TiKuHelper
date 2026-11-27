package uapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUAPIAuditParsesPublicCallOutcomesWithoutPayloads(t *testing.T) {
	const key = "uapi-audit-private-key-qa"
	const rawText = "audit-sensitive-text-qa"
	const rawID = "audit-upstream-private-id-qa"
	const taskID = "audit-private-task-qa"
	const phone = "13800138000"
	const rawURL = "https://owned.invalid/audit-test-qa"
	const inputError = "invalid ip with spaces audit-private"
	bodies := map[string]any{
		"/phone":         map[string]any{"city": rawText},
		"/ocr":           map[string]any{"text": rawText, "plain_text": rawText},
		"/words":         map[string]any{"status": "forbidden", "original_text": rawText, "masked_text": rawText, "forbidden_words": []string{rawText}},
		"/engines":       map[string]any{"engine": map[string]any{"name": rawText}, "limits": map[string]any{"default": 10, "max": 100}, "supported_parameters": []string{rawText}},
		"/search":        map[string]any{"query": rawText, "total_results": 1, "results": []map[string]any{{"title": rawText, "url": rawURL}}, "sources": []map[string]any{}, "process_time_ms": 1, "metadata": map[string]any{}},
		"/web":           map[string]any{"task_id": taskID, "status": "pending", "url": rawURL},
		"/web/" + taskID: map[string]any{"task_id": taskID, "status": "completed", "result": map[string]any{"markdown": rawText}},
	}

	var total atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		w.Header().Set("X-Request-ID", rawID)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": rawText})
		case "/invalid-response":
			fmt.Fprint(w, "<"+rawText+">")
		default:
			payload, ok := bodies[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(payload)
		}
	}))
	defer s.Close()
	c := NewClient(key).SetPhoneInfoURL(s.URL + "/phone").SetImageOCRURL(s.URL + "/ocr").
		SetProfanityCheckURL(s.URL + "/words").SetSearchEnginesURL(s.URL + "/engines").
		SetSearchAggregateURL(s.URL + "/search").SetWebToMarkdownURL(s.URL + "/web").SetMaxRetries(2)
	var captured bytes.Buffer
	old := gin.DefaultWriter
	gin.DefaultWriter = &captured
	t.Cleanup(func() { gin.DefaultWriter = old })
	ctx := context.Background()
	if _, err := c.GetPhoneInfo(ctx, phone); err != nil {
		t.Fatalf("phone transport fixture: %v", err)
	}
	if _, err := c.ImageOCR(ctx, ImageOCRRequest{Image: []byte(rawText), ImageName: "fixture.png"}); err != nil {
		t.Fatalf("ocr transport fixture: %v", err)
	}
	if v, err := c.CheckProfanity(ctx, ProfanityCheckRequest{Text: rawText}); err != nil || !v.Forbidden() {
		t.Fatalf("profanity fixture: %v", err)
	}
	if _, err := c.GetSearchEngines(ctx); err != nil {
		t.Fatalf("engines fixture: %v", err)
	}
	if _, err := c.SearchAggregate(ctx, SearchAggregateRequest{Query: rawText}); err != nil {
		t.Fatalf("search fixture: %v", err)
	}
	if v, err := c.SubmitWebToMarkdown(ctx, rawURL); err != nil || v.TaskID != taskID {
		t.Fatalf("web submit fixture: %v", err)
	}
	if v, err := c.GetWebToMarkdownStatus(ctx, taskID); err != nil || v.Status != "completed" {
		t.Fatalf("web poll fixture: %v", err)
	}
	if _, err := c.SetImageOCRURL(s.URL+"/forbidden").ImageOCR(ctx, ImageOCRRequest{Image: []byte(rawText), ImageName: "fixture.png"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected existing forbidden classification: %v", err)
	}
	if _, err := c.SetImageOCRURL(s.URL+"/invalid-response").ImageOCR(ctx, ImageOCRRequest{Image: []byte(rawText), ImageName: "fixture.png"}); !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected existing invalid-response classification: %v", err)
	}
	if _, err := c.GetIPInfo(ctx, inputError, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected local rejection: %v", err)
	}
	if _, err := c.CheckProfanity(ctx, ProfanityCheckRequest{Text: "  "}); err != nil {
		t.Fatalf("expected no-request success: %v", err)
	}
	if total.Load() != 9 {
		t.Fatalf("audit changed existing wire count, got %d, want 9", total.Load())
	}
	type expected struct {
		category, outcome, errorClass string
		http, attempts                int
		key                           string
		count                         int
	}
	want := []expected{
		{"phone_lookup", "success", "", 200, 1, "has_city", 1},
		{"ocr", "success", "", 200, 1, "has_text", 1},
		{"profanity", "matched", "", 200, 1, "matched_count", 1},
		{"search_engines", "success", "", 200, 1, "has_engine", 1},
		{"search_aggregate", "success", "", 200, 1, "result_count", 1},
		{"web_submit", "pending", "", 200, 1, "has_task", 1},
		{"web_poll", "completed", "", 200, 1, "has_result", 1},
		{"ocr", "error", "forbidden", 403, 1, "", 0},
		{"ocr", "error", "invalid_response", 200, 1, "", 0},
		{"ip_lookup", "error", "invalid_argument", 0, 0, "", 0},
		{"profanity", "no_request", "", 0, 0, "matched_count", 0},
	}
	all := captured.String()
	for _, p := range []string{key, rawText, rawID, taskID, rawURL, phone, inputError, "fixture.png"} {
		if strings.Contains(all, p) {
			t.Fatal("audit emitted a private input, payload, filename, or upstream identity")
		}
	}
	var events []providerAuditEvent
	for _, line := range strings.Split(all, "\n") {
		const prefix = "uapi_call "
		at := strings.Index(line, prefix)
		if at < 0 {
			continue
		}
		var event providerAuditEvent
		if err := json.Unmarshal([]byte(line[at+len(prefix):]), &event); err != nil {
			t.Fatalf("bad JSON audit: %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 2*len(want) {
		t.Fatalf("got %d events; expected %d", len(events), 2*len(want))
	}
	rawDigest := sha256.Sum256([]byte(rawID))
	correlationHint := events[1].UpstreamIDHash
	if hint, err := hex.DecodeString(correlationHint); err != nil || len(hint) != sha256.Size || correlationHint == hex.EncodeToString(rawDigest[:]) {
		t.Fatal("untrusted provider ID is not represented by a keyed opaque hint")
	}
	localIDs := map[string]bool{}
	for i, x := range want {
		a, b := events[2*i], events[2*i+1]
		h := ""
		if x.attempts > 0 {
			h = correlationHint
		}
		if a.Phase != "start" || b.Phase != "finish" || a.Category != x.category || b.Category != x.category ||
			a.ID == "" || a.ID != b.ID || localIDs[a.ID] || b.Outcome != x.outcome ||
			b.ErrorClass != x.errorClass || b.HTTPStatus != x.http || b.Attempts != x.attempts || b.Retries != 0 || b.UpstreamIDHash != h {
			t.Errorf("call %d (%s) unexpected audit phases or terminal metadata: %+v %+v", i, x.category, a, b)
		}
		localIDs[a.ID] = true
		if x.key == "" {
			if len(b.Summary) != 0 {
				t.Errorf("error operation %d emitted business result summary", i)
			}
		} else if n, ok := b.Summary[x.key]; !ok || n != x.count {
			t.Errorf("operation %d summary %s missing or wrong", i, x.key)
		}
	}
}
