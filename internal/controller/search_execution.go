package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

type searchExecutionResult struct {
	status int
	body   []byte
	header http.Header
}

const searchDeadlineRequestsKey = "tiku.search.execution.deadline-requests"

// The producer receives no reference to the observer's response writer or
// pooled Gin context. Request.Clone preserves values/address and copies URL and
// headers; Body/GetBody must be independently recreated from the captured bytes.
func snapshotSearchExecution(c *gin.Context, rawBody string, ctx context.Context) (*gin.Context, *httptest.ResponseRecorder, chan struct{}) {
	recorder := httptest.NewRecorder()
	bufferContext, _ := gin.CreateTestContext(recorder)
	worker := c.Copy()
	worker.Writer = bufferContext.Writer
	worker.Request = c.Request.Clone(ctx)
	worker.Request.Body = io.NopCloser(bytes.NewBufferString(rawBody))
	worker.Request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString(rawBody)), nil
	}
	deadlineRequests := make(chan struct{}, 1)
	worker.Set(searchDeadlineRequestsKey, (chan<- struct{})(deadlineRequests))
	return worker, recorder, deadlineRequests
}

func bufferedSearchExecutionResult(recorder *httptest.ResponseRecorder) searchExecutionResult {
	return searchExecutionResult{status: recorder.Code, body: append([]byte(nil), recorder.Body.Bytes()...), header: recorder.Header().Clone()}
}

func unavailableSearchExecutionResult() searchExecutionResult {
	return searchExecutionResult{status: http.StatusServiceUnavailable, body: []byte(`{"message":"搜题已执行，但结果暂无法确认，请勿重复提交"}`), header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}}
}

// Only the HTTP goroutine accesses its original writer. A buffered signal keeps
// the original AI-only deadline decision effective without blocking a producer
// after the observer has left. Keyed callers retain their existing outer clear.
func requestSearchWriteDeadlineClear(c *gin.Context) {
	if value, ok := c.Get(searchDeadlineRequestsKey); ok {
		if requests, ok := value.(chan<- struct{}); ok {
			select {
			case requests <- struct{}{}:
			default:
			}
			return
		}
	}
	clearAIResponseWriteDeadline(c)
}

func observeSearchExecution(c *gin.Context, deadlineRequests <-chan struct{}, execute func() searchExecutionResult) {
	done := make(chan searchExecutionResult, 1)
	release := searchExecutionOwners.start() // Register before this HTTP caller can return.
	go func() {
		defer release()
		result := unavailableSearchExecutionResult()
		defer func() {
			if recover() != nil {
				// Never expose arbitrary panic strings, payloads, or credentials.
				logger.SysError("搜题后台执行发生异常，结果待核对")
				result = unavailableSearchExecutionResult()
			}
			done <- result // Buffered: observer exit never blocks completion/settlement.
		}()
		result = execute()
	}()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-deadlineRequests:
			clearAIResponseWriteDeadline(c)
		case result := <-done:
			if c.Request.Context().Err() != nil {
				return
			}
			// Both channels may be ready together. Preserve the queued AI clear
			// before writing the response, even if the producer finished quickly.
			select {
			case <-deadlineRequests:
				clearAIResponseWriteDeadline(c)
			default:
			}
			for name, values := range result.header {
				c.Writer.Header().Del(name)
				for _, value := range values {
					c.Writer.Header().Add(name, value)
				}
			}
			c.Data(result.status, "", result.body)
			return
		}
	}
}

type searchExecutionOwnerTracker struct {
	mu     sync.Mutex
	active int
	idle   chan struct{}
}

func newSearchExecutionOwnerTracker() *searchExecutionOwnerTracker {
	idle := make(chan struct{})
	close(idle)
	return &searchExecutionOwnerTracker{idle: idle}
}

func (t *searchExecutionOwnerTracker) start() func() {
	t.mu.Lock()
	if t.active == 0 {
		t.idle = make(chan struct{})
	}
	t.active++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.active--
			if t.active == 0 {
				close(t.idle)
			}
		})
	}
}

func (t *searchExecutionOwnerTracker) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idle:
		return ctx.Err()
	}
}

var searchExecutionOwners = newSearchExecutionOwnerTracker()

// WaitForSearchExecutions is called after HTTP Shutdown has stopped admissions
// and joined handlers. Reuse its remaining context budget, never add a timeout
// or use shutdown as a signal to cancel already admitted paid work.
func WaitForSearchExecutions(ctx context.Context) error {
	return searchExecutionOwners.wait(ctx)
}
