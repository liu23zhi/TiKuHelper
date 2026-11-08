package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/diagnostics"

	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// fallbackFlightPayload 是一次合并 AI 兜底的结果。
//
// 这里刻意用强类型而不是序列化成字符串：service.AIFallbackResult 的
// Usage / Model / UnpricedModel 都带 `json:"-"`，走 JSON 会**静默丢字段**，
// 而 Usage 正是计费依据 —— 丢了会让搭车者拿到没有用量的结果。
type fallbackFlightPayload struct {
	result *service.AIFallbackResult
}

// fallbackFlights 合并「同一用户 + 完全相同题目」的并发 AI 兜底请求。
//
// 兜底链路同样按「一次上游调用 = 一次计费」计费：并发提交相同题目时，
// 若不合并，每个请求都会各自调用一次 AI 并各自扣一次费。
// 与搜题一致，只有用户、题目、类型、选项**完全一致**才会合并。
var fallbackFlights = newFlightGroup[fallbackFlightPayload]()

// observeFallbackFlightFollower is nil in production; tests may observe a real
// follower joining this exact shared call before releasing a synthetic provider.
var observeFallbackFlightFollower func(string, *flightCall[fallbackFlightPayload])

// buildFallbackFlightKey 构造兜底合并的 key。
//
// 用 SHA-256 而不是把原文直接当 key：兜底请求的 options 可能很长
// （线上实测 55 条、含大量编辑器脚本），直接内联会让 map key 膨胀到数 KB。
// 哈希只用于压缩 key，不影响「完全一致才合并」的语义。
func buildFallbackFlightKey(userID int32, req model.SearchRequest, actorScope ...string) string {
	payload, err := json.Marshal(struct {
		UserID           int32                   `json:"userId"`
		ActorScope       string                  `json:"actorScope,omitempty"`
		Question         string                  `json:"question"`
		Type             int                     `json:"type"`
		DeepThinking     bool                    `json:"deepThinking"`
		Options          []string                `json:"options"`
		QuestionEvidence *model.QuestionEvidence `json:"questionEvidence,omitempty"`
	}{
		UserID:           userID,
		ActorScope:       firstAIFallbackActorScope(actorScope),
		Question:         strings.TrimSpace(req.Question),
		Type:             req.Type,
		DeepThinking:     req.DeepThinking,
		Options:          req.Options,
		QuestionEvidence: req.QuestionEvidence,
	})
	if err != nil {
		// 理论上 json.Marshal 对这几个类型不会失败；退化为仅按题目合并，
		// 宁可少合并也不能 panic。
		return strings.TrimSpace(req.Question)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// beginFallbackFlight 尝试成为某个 key 的兜底发起者。
//
//   - isLeader=true：调用方必须执行兜底查询，完成后调用 finishFallbackFlight 广播。
//   - isLeader=false 且 borrowed 非 nil：复用了他人结果，**不要再计费**。
//   - isLeader=false 且 borrowed 为 nil：等待超时或发起者无结果，调用方自行继续。
func beginFallbackFlight(key string) (isLeader bool, borrowed *service.AIFallbackResult, call *flightCall[fallbackFlightPayload]) {
	leader, shared := fallbackFlights.begin(key)
	if leader != nil {
		return true, nil, leader
	}

	if observeFallbackFlightFollower != nil {
		observeFallbackFlightFollower(key, shared)
	}
	return false, waitFallbackFlightResult(key, shared, defaultFlightWaitTimeout), nil
}

// waitFallbackFlightResult preserves the existing delivery decision while
// recording shared only for a successfully borrowed result, not a failed one.
func waitFallbackFlightResult(key string, shared *flightCall[fallbackFlightPayload], timeout time.Duration) *service.AIFallbackResult {
	payload, ok := fallbackFlights.wait(key, shared, timeout)
	if !ok || payload.result == nil {
		return nil
	}
	if payload.result.Success {
		diagnostics.Default.ObserveCache("fallback", "shared")
	}
	return payload.result
}

// finishFallbackFlight 广播兜底结果并释放 key。幂等，可安全地由 defer 兜底再调用一次。
func finishFallbackFlight(key string, call *flightCall[fallbackFlightPayload], result *service.AIFallbackResult) {
	if result == nil {
		fallbackFlights.finish(key, call, fallbackFlightPayload{}, false)
		return
	}
	fallbackFlights.finish(key, call, fallbackFlightPayload{result: result}, true)
}
