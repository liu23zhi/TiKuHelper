package controller

import (
	"context"
	"time"

	"github.com/itihey/tikuAdapter/internal/diagnostics"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// searchFlightPayload 是一次合并搜题的结果：答案 + 是否可用。
type searchFlightPayload struct {
	answers  [][]string
	terminal *searchFlightTerminal
}

// terminal is an already executed, recorded-usage no-answer outcome, not a
// reusable answer cache entry. Its presence is independent of Error: a paid
// model can legitimately return no answer without reporting an upstream error.
// Only public response fields cross the cohort; no owner token/body/ID does.
type searchFlightTerminal struct {
	status      string
	publicError *model.SearchError
	warnings    []string
}

func cloneSearchFlightTerminal(value *searchFlightTerminal) *searchFlightTerminal {
	if value == nil {
		return nil
	}
	copy := &searchFlightTerminal{status: value.status, warnings: append([]string(nil), value.warnings...)}
	if value.publicError != nil {
		errorCopy := *value.publicError
		copy.publicError = &errorCopy
	}
	return copy
}

// searchFlights 合并「同一用户 + 完全相同的题目与选项」的并发搜题请求。
//
// 合并的粒度是**缓存 key**：只有用户、题目、选项、缓存代次完全一致才会合并，
// 与缓存复用条件保持严格一致 —— 任何一个字段不同都不会搭车。
var searchFlights = newFlightGroup[searchFlightPayload]()

// beginSearchFlight 尝试成为某个 key 的发起者。
//
//   - isLeader=true：调用方必须执行上游查询，完成后调用 finishSearchFlight 广播结果。
//   - isLeader=false 且 borrowed 非 nil：复用了他人结果（不应计费）。
//   - isLeader=false 且 borrowed 为 nil：等待超时或发起者无答案，调用方自行继续。
func beginSearchFlight(key string) (isLeader bool, borrowed [][]string, call *flightCall[searchFlightPayload]) {
	isLeader, borrowed, call, _ = beginSearchFlightWithContext(context.Background(), key)
	return
}

func beginSearchFlightWithContext(ctx context.Context, key string) (bool, [][]string, *flightCall[searchFlightPayload], error) {
	leader, answers, _, call, err := beginSearchFlightOutcomeWithContext(ctx, key)
	return leader, answers, call, err
}

func beginSearchFlightOutcomeWithContext(ctx context.Context, key string) (bool, [][]string, *searchFlightTerminal, *flightCall[searchFlightPayload], error) {
	leader, shared := searchFlights.begin(key)
	if leader != nil {
		return true, nil, nil, leader, nil
	}
	answers, terminal, err := waitSearchFlightOutcomeWithContext(ctx, key, shared, defaultFlightWaitTimeout)
	return false, answers, terminal, nil, err
}

// waitSearchFlightResult records shared only after a usable result is borrowed,
// never when joining a call, timing out, or receiving an empty result.
func waitSearchFlightResult(key string, shared *flightCall[searchFlightPayload], timeout time.Duration) [][]string {
	answers, _ := waitSearchFlightResultWithContext(context.Background(), key, shared, timeout)
	return answers
}

func waitSearchFlightResultWithContext(ctx context.Context, key string, shared *flightCall[searchFlightPayload], timeout time.Duration) ([][]string, error) {
	answers, _, err := waitSearchFlightOutcomeWithContext(ctx, key, shared, timeout)
	return answers, err
}

func waitSearchFlightOutcomeWithContext(ctx context.Context, key string, shared *flightCall[searchFlightPayload], timeout time.Duration) ([][]string, *searchFlightTerminal, error) {
	payload, ok, err := searchFlights.waitContext(ctx, key, shared, timeout)
	if err != nil || !ok {
		return nil, nil, err
	}
	if !hasUsableSearchAnswers(payload.answers) {
		return nil, cloneSearchFlightTerminal(payload.terminal), nil
	}
	// Executed no-answer delivery must not fabricate a usable/cache shared hit.
	diagnostics.Default.ObserveCache("search", "shared")
	return cloneSearchAnswers(payload.answers), nil, nil
}

// finishSearchFlight 广播搜题结果并释放 key。幂等，可安全地由 defer 兜底再调用一次。
func finishSearchFlight(key string, call *flightCall[searchFlightPayload], answers [][]string) {
	finishSearchFlightOutcome(key, call, answers, nil)
}

func finishSearchFlightOutcome(key string, call *flightCall[searchFlightPayload], answers [][]string, terminal *searchFlightTerminal) {
	usable := hasUsableSearchAnswers(answers)
	if usable {
		terminal = nil
	}
	searchFlights.finish(key, call, searchFlightPayload{answers: cloneSearchAnswers(answers), terminal: cloneSearchFlightTerminal(terminal)}, usable || terminal != nil)
}
