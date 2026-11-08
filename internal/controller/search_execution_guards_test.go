package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// These guards concern the newly introduced owner/snapshot/writer boundary.
// They are MODIFIED-only; not extra passing cases in the same-input B/M/R.
func TestSearchExecutionSnapshotOwnsRequestAndWriter(t *testing.T) {
	original, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), guardContextKey{}, "preserved"))
	defer cancel()
	original.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=icodef", strings.NewReader(`{"question":"snapshot"}`)).WithContext(ctx)
	original.Request.Header.Set("X-Synthetic", "before")
	original.Request.RemoteAddr = "192.0.2.12:12345"
	original.Set("owned", "before")
	worker, recorder, _ := snapshotSearchExecution(original, `{"question":"snapshot"}`, context.WithoutCancel(ctx))
	original.Request.URL.RawQuery = "changed=1"
	original.Request.Header.Set("X-Synthetic", "after")
	original.Request.Body = io.NopCloser(strings.NewReader("later-body"))
	original.Request.RemoteAddr = "192.0.2.99:54321"
	original.Set("owned", "after")
	cancel()
	if worker.Request.Context().Err() != nil || worker.Request.Context().Value(guardContextKey{}) != "preserved" {
		t.Fatal("admitted owner lost values or inherited observer cancellation")
	}
	if worker.Request.URL.RawQuery != "use=icodef" || worker.Request.Header.Get("X-Synthetic") != "before" || worker.GetString("owned") != "before" {
		t.Fatal("worker request/keys alias the returned HTTP context")
	}
	if worker.Request.RemoteAddr != "192.0.2.12:12345" {
		t.Fatal("producer lost its admitted request address")
	}
	firstBody, firstErr := io.ReadAll(worker.Request.Body)
	recreated, recreateErr := worker.Request.GetBody()
	if firstErr != nil || recreateErr != nil {
		t.Fatal("owned body/GetBody unavailable")
	}
	secondBody, secondErr := io.ReadAll(recreated)
	_ = recreated.Close()
	if secondErr != nil || string(firstBody) != `{"question":"snapshot"}` || string(secondBody) != string(firstBody) {
		t.Fatal("producer body aliases returned HTTP stream or GetBody cannot recreate it")
	}
	worker.Header("X-Worker", "isolated")
	worker.JSON(http.StatusAccepted, gin.H{"message": "snapshot"})
	if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), "snapshot") || original.Writer.Header().Get("X-Worker") != "" {
		t.Fatal("producer did not use its independent writer")
	}
}

type guardContextKey struct{}

func TestSearchExecutionOwnerDrainTracksDetachedWork(t *testing.T) {
	tracker := newSearchExecutionOwnerTracker()
	release := tracker.start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(tracker.wait(ctx), context.Canceled) {
		t.Fatal("active owner cannot be called drained after caller budget ends")
	}
	release()
	if err := tracker.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A pre-ended shutdown budget must never report success merely because
	// no owner happened to be registered at the instant of the check.
	if !errors.Is(tracker.wait(ctx), context.Canceled) {
		t.Fatal("expired shutdown context incorrectly reported graceful completion")
	}
}

func TestSearchExecutionObservedHeadersAndSafePanic(t *testing.T) {
	for _, panicCase := range []bool{false, true} {
		t.Run(map[bool]string{false: "status-headers-body", true: "fixed-safe-panic"}[panicCase], func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search", nil)
			c.Header("X-Middleware", "retained")
			observeSearchExecution(c, nil, func() searchExecutionResult {
				if panicCase {
					panic("synthetic-sensitive-panic-must-not-be-returned")
				}
				return searchExecutionResult{status: http.StatusAccepted, body: []byte(`{"message":"owned"}`), header: http.Header{"Content-Type": {"application/json; charset=utf-8"}, "X-Owned": {"one", "two"}}}
			})
			if recorder.Header().Get("X-Middleware") != "retained" {
				t.Fatal("buffered result overwrote middleware headers")
			}
			if panicCase {
				if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "synthetic-sensitive") || recorder.Body.Len() == 0 {
					t.Fatal("panic escaped or was falsely returned as successful empty200")
				}
			} else if recorder.Code != http.StatusAccepted || recorder.Body.String() != `{"message":"owned"}` || len(recorder.Header().Values("X-Owned")) != 2 {
				t.Fatal("status, body, or multivalue header was not preserved")
			}
			if err := WaitForSearchExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type searchDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *searchDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestSearchExecutionClearsOnlyOriginalAIWriterDeadline(t *testing.T) {
	oldResolver, oldBindings, oldAI, oldReporter := resolveSearchRequestContext, buildSearchClientBindings, searchAIAnswer, searchBillingReporter
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	defer func() {
		if err := WaitForSearchExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		resolveSearchRequestContext, buildSearchClientBindings, searchAIAnswer, searchBillingReporter = oldResolver, oldBindings, oldAI, oldReporter
		gin.SetMode(oldMode)
	}()
	resolveSearchRequestContext = func(_ context.Context, _ string, _ string, query url.Values) (resolvedSearchRequestContext, error) {
		resolved := resolvedSearchRequestContext{SearchMode: searchModeNormal, Tokens: map[string]string{"icodef": "synthetic-not-a-credential"}}
		if query.Get("use") == "ai" {
			resolved.SearchMode = searchModeAI
			resolved.AIConfig = service.AISearchRequestConfig{BaseURL: "https://synthetic.invalid", APIKey: "synthetic-not-a-credential", Model: "synthetic-model"}
		}
		return resolved, nil
	}
	buildSearchClientBindings = func(resolvedSearchRequestContext, string) []searchClientBinding {
		return []searchClientBinding{{Provider: "icodef", Search: func(model.SearchRequest) ([][]string, error) { return [][]string{{"normal-answer"}}, nil }}}
	}
	searchAIAnswer = func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
		return [][]string{{"ai-answer"}}, searchpkg.AISearchUsage{}, nil
	}
	searchBillingReporter = func(searchBillingContext) {}
	for _, use := range []string{"icodef", "ai"} {
		writer := &searchDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use="+use+"&noRecord=1", strings.NewReader(`{"question":"writer-guard","skipCache":true}`))
		c.Request.Header.Set("Content-Type", "application/json")
		Search(c)
		want := 0
		if use == "ai" {
			want = 1
		}
		if writer.Code != http.StatusOK || writer.Body.Len() == 0 || len(writer.deadlines) != want {
			t.Fatalf("use=%s status=%d originalWriterDeadlineCalls=%d want=%d", use, writer.Code, len(writer.deadlines), want)
		}
		for _, deadline := range writer.deadlines {
			if !deadline.IsZero() {
				t.Fatal("AI writer did not receive the original zero deadline clear")
			}
		}
	}
}

func TestSearchExecutionDetachedHTTPKeepsOwnerUntilFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search", nil).WithContext(ctx)
	entered, release, finalized, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(handlerDone)
		observeSearchExecution(c, nil, func() searchExecutionResult {
			close(entered)
			<-release
			close(finalized) // Stands for completed owner-side persistence, not SQL proof.
			return unavailableSearchExecutionResult()
		})
	}()
	var released bool
	defer func() {
		if !released {
			close(release)
		}
		<-handlerDone
		_ = WaitForSearchExecutions(context.Background())
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("owned execution not entered")
	}
	cancel()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP observer retained producer wait")
	}
	searchExecutionOwners.mu.Lock()
	active := searchExecutionOwners.active
	searchExecutionOwners.mu.Unlock()
	if active != 1 {
		t.Fatalf("owner lost after observer exit: active=%d", active)
	}
	select {
	case <-finalized:
		t.Fatal("producer finalized before fixture release")
	default:
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- WaitForSearchExecutions(context.Background()) }()
	close(release)
	released = true
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-finalized:
		default:
			t.Fatal("owner drain preceded finalization")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner drain did not join finalized execution")
	}
}
