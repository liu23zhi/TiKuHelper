package controller

// searchFlightPayload 是一次合并搜题的结果：答案 + 是否可用。
type searchFlightPayload struct {
	answers [][]string
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
	leader, shared := searchFlights.begin(key)
	if leader != nil {
		return true, nil, leader
	}

	payload, ok := searchFlights.wait(key, shared, defaultFlightWaitTimeout)
	if !ok {
		return false, nil, nil
	}
	if !hasUsableSearchAnswers(payload.answers) {
		// 发起者没拿到可用答案。
		return false, nil, nil
	}
	return false, cloneSearchAnswers(payload.answers), nil
}

// finishSearchFlight 广播搜题结果并释放 key。幂等，可安全地由 defer 兜底再调用一次。
func finishSearchFlight(key string, call *flightCall[searchFlightPayload], answers [][]string) {
	searchFlights.finish(key, call, searchFlightPayload{answers: cloneSearchAnswers(answers)}, hasUsableSearchAnswers(answers))
}
