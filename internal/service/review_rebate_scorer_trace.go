package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

// 评价返现 Agent 打分的详细判断追踪（task_doc/73 §3.2）。
//
// # 为什么需要这份追踪
//
// 既有实现只在**错误**与**脱敏触发**时写日志（review_rebate_scorer.go 里
// 三处 logger 调用），成功路径的判断过程完全没有记录。而运营最需要复盘的
// 恰恰是成功路径：「这条为什么给 0.8 元而不是 1.0 元」。
//
// # 写到哪里
//
// logger.ReviewRebateTrace → Tiku-review-rebate.txt（独立文件）。
// 不复用 Tiku-thinking.txt —— 那是 AI **搜题**的通道，混用会让两类日志
// 都无法按主题检索。
//
// # 隐私边界
//
// 记：claimId / userId / 星级 / 内容分 / 置信度 / 理由文本 / 提示词 / 原始输出。
// 不记：API Key（RedactSecrets 兜底，且不主动拼）、截图二进制（体积大且是私产）、
//       手机号 / 微信 OpenID（属个人标识）。
// userId 是内部数字 ID，不直接暴露个人信息。

// reviewRebateTraceContext 携带一次打分的**过程信息**，供成功路径在
// 写库之后补写 trace 时使用。
//
// 为什么要有这个类型：ok 路径的 matchedRuleId / awardAmount 只有
// ScoreReviewRebateClaimWithDB 的事务里算完规则才存在，而 agentReviewRebateScorer.Score
// 手里没有它们。把「过程信息」与「结果快照」拆开，前者由打分器产出、
// 后者从落库行读 —— 两者的时间点不同，混在一个结构里就会写出假数据。
type reviewRebateTraceContext struct {
	Model        string
	Attempt      int
	SystemPrompt string
	UserPrompt   string
	RawResponse  string
	DurationMs   int64
	StartedAt    time.Time
}

// reviewRebateTraceRecord 是写入 trace 的一条记录。
type reviewRebateTraceRecord struct {
	Claim  entity.ReviewRebateClaim
	Scorer string
	Model  string
	// Attempt 是**同一个 claim 的第几次打分**（首次 = 1）。
	//
	// 为什么必须记（task_doc/45 §8.2 的缺口）：claim 表没有 attempt 列，
	// 管理员对同一条评价反复重跑后，日志里几条记录无法区分先后 ——
	// 「先给 0.3 后给 0.8」与「先给 0.8 后给 0.3」是完全不同的故事。
	Attempt    int
	DurationMs int64
	// StartedAt 是本条 trace 的**发生时刻**，序列化为 payload 的 "ts"。
	//
	// 为什么要在 payload 里再记一次时间（日志行首已经有时间戳）：
	// 行首时间是 `logger.ReviewRebateTrace` **写入**的时刻，而 StartedAt 是
	// **打分开始**的时刻。两者在超时/排队场景下会差到分钟级，
	// 排查「这条为什么慢」必须能拿到后者。零值由调用方保证不发生
	// （见 traceReviewRebateScoring 的兜底）。
	StartedAt time.Time
	// Sanitized 标记本条 trace 是否**已脱敏**，序列化为 payload 的 "sanitized"。
	//
	// 值来自真实判定而不是常量：writeDedicatedFile 里 RedactSecrets 是否
	// 真的改写了内容，只有比较改写前后才知道。这里由
	// reviewRebateTraceWasSanitized 计算（见下）。
	Sanitized bool
	// MatchedRuleID 是打分时命中的返现规则 id（0 = 未命中任何规则）。
	//
	// 来源：claim 行上的快照列。打分本身不选规则 —— 选规则发生在
	// ScoreReviewRebateClaimWithDB 的事务内（review_rebate_claim.go）。
	// 因此成功路径读到的是本次刚写入的值；error / parse_error 路径读到的是
	// **上一次**的值（或 0）。这个语义在 payload 里靠 outcome 一起读才不歧义，
	// 所以两者必须同时存在。
	MatchedRuleID int32
	// AwardAmount 是**候选**发放金额（命中规则时的金额，0 = 未算或未命中）。
	//
	// 与 MatchedRuleID 同源同语义：都是「打分完成后写进 claim 的快照」，
	// 不是本函数算出来的。真正发钱的是管理员批准那一步。
	AwardAmount float64
	// SystemPrompt 是系统提示词**全文**。
	//
	// 2026-11-11 用户要求「都保留完整提示词，回复记录等」（task_doc/45 §0.26.1、
	// task_doc/77 §11.1），覆盖了 §8.3「只记 sha256」的旧口径：
	// 排查「模型是不是被提示词带偏了」必须能看到原文，sha256 只能判断版本变没变。
	// 全文仍然保留 sha256 字段作快速比对，成本几乎为零。
	SystemPrompt string
	UserPrompt   string
	RawResponse  string
	Parsed       *ReviewRebateScore
	// Outcome 取值：ok / error / parse_error / no_screenshot。
	Outcome string
	Error   string
}

// 同一 claim 的打分次数计数器（进程内）。
//
// 口径：首次打分 = 1；同一 claim 每再打一次 +1。
// 进程重启后如果 claim 上已经有打分痕迹（AgentScorer / AgentStars / 置信度），
// 本次从 2 起计 —— 精确到「第几次」需要落库（claim 表无计数列，
// 改表属 task_doc/45 §8.2 的后续项），但「首次 vs 重跑」以及
// 「同一进程内前后顺序」都能区分。
//
// 每个 claim 只存一个 int，量级与被打分过的 claim 数同级（低频操作），不做淘汰。
var (
	reviewRebateAttemptMu  sync.Mutex
	reviewRebateAttemptSeq = map[int32]int{}
)

// nextReviewRebateScoreAttempt 返回本次打分应记的 attempt 序号。
func nextReviewRebateScoreAttempt(claim entity.ReviewRebateClaim) int {
	reviewRebateAttemptMu.Lock()
	defer reviewRebateAttemptMu.Unlock()
	current, seen := reviewRebateAttemptSeq[claim.ID]
	if !seen && reviewRebateWasScoredBefore(claim) {
		current = 1
	}
	next := current + 1
	reviewRebateAttemptSeq[claim.ID] = next
	return next
}

// reviewRebateWasScoredBefore 判断这条 claim 在本进程之前是否已经拿过分数。
func reviewRebateWasScoredBefore(claim entity.ReviewRebateClaim) bool {
	return strings.TrimSpace(claim.AgentScorer) != "" || claim.AgentStars > 0 || claim.AgentConfidence > 0
}

// traceReviewRebateScoring 把一条打分记录序列化成 JSON Lines 写进独立日志。
//
// JSON Lines（一行一条）而不是 pretty JSON：便于 grep、便于按行切分解析，
// 且几十万行也不会让文件无法处理。
//
// 本函数**不得**返回错误或 panic —— 追踪失败绝不能影响打分主流程。
func traceReviewRebateScoring(record reviewRebateTraceRecord) {
	// 系统提示词按全文记录（用户 2026-11-11 要求「都保留完整提示词」）；
	// 调用方偶尔不传时退回本仓唯一的那份常量，保证每条记录都能自解释。
	systemPrompt := strings.TrimSpace(record.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = ReviewRebateAgentSystemPrompt
	}
	payload := map[string]any{
		"claimId":   record.Claim.ID,
		"userId":    record.Claim.UserID,
		"programId": record.Claim.ProgramID,
		"scorer":    record.Scorer,
		"model":     record.Model,
		"outcome":   record.Outcome,
		// ts：本条 trace 的**发生时刻**（打分开始时），不是日志写入时刻。
		// 与日志行首时间戳是两个不同精度的事实，排查超时/排队必须用这个。
		// 格式与日志行首一致，便于同一份日志里按文本也能对齐。
		"ts": reviewRebateTraceTimestamp(record.StartedAt),
		// sanitized：本条 trace 写入前是否真的被 RedactSecrets 改写过。
		// 由 reviewRebateTraceWasSanitized 比较改写前后得出，不是常量。
		"sanitized": record.Sanitized,
		// attempt：同一 claim 的第几次打分，用于区分重跑先后（见 reviewRebateTraceRecord）。
		"attempt": record.Attempt,
		// 系统提示词**全文** + sha256：全文供复盘提示词是否被带偏，
		// sha256 供快速比对「换没换版本」（16 位足够）。
		"systemPrompt":       systemPrompt,
		"systemPromptSha256": reviewRebatePromptDigest(systemPrompt),
		"durationMs":         record.DurationMs,
		"claimedStars":       record.Claim.ClaimedStars,
		"claimedReviewUrl":   strings.TrimSpace(record.Claim.ClaimedReviewURL),
		// matchedRuleId / awardAmount：打分后写进 claim 的规则快照与候选金额。
		// 0 的含义随 outcome 变化（未命中规则 / 尚未算到那一步），
		// 所以这两个字段必须与 outcome 一起读。
		"matchedRuleId": record.MatchedRuleID,
		"awardAmount":   record.AwardAmount,
	}
	if prompt := strings.TrimSpace(record.UserPrompt); prompt != "" {
		payload["userPrompt"] = prompt
	}
	if raw := strings.TrimSpace(record.RawResponse); raw != "" {
		payload["rawResponse"] = raw
	}
	if record.Parsed != nil {
		payload["parsed"] = map[string]any{
			"stars":        record.Parsed.Stars,
			"contentScore": record.Parsed.ContentScore,
			"confidence":   record.Parsed.Confidence,
			"rationale":    record.Parsed.Rationale,
		}
	}
	if record.Error != "" {
		payload["error"] = record.Error
	}

	// sanitized 的唯一真实来源：把**将要写入日志的这份 payload** 序列化后
	// 过一遍 RedactSecrets，看有没有被改写。
	//
	// 为什么要在这里算而不是让调用方传：脱敏发生在 logger 内部
	// （writeDedicatedFile → RedactSecrets），调用方并不持有「脱敏后」的
	// 文本。让调用方猜一个 bool 就是占位常量 —— 那正是本任务要消除的东西。
	//
	// 判据是「改写前后是否逐字节相同」，不是「文本里有没有出现密钥字样」：
	// 后者会把「报文里本来就写着 Authorization: Bearer」的正常内容
	// 误判成发生过脱敏。
	//
	// 两遍 Marshal：第一遍不带 sanitized 位得到待判定文本，第二遍写最终结果。
	// 成本可忽略（一条 trace 几十 KB，这个字段只影响一个 bool）。
	withoutSanitized, err := json.Marshal(payload)
	if err != nil {
		logger.SysWarn("评价返现 trace 序列化失败: " + err.Error())
		return
	}
	payload["sanitized"] = reviewRebateTraceWasSanitized(string(withoutSanitized))

	encoded, err := json.Marshal(payload)
	if err != nil {
		// 序列化失败不该发生（全是基础类型），真发生了也只记一条简短说明。
		logger.SysWarn("评价返现 trace 序列化失败: " + err.Error())
		return
	}
	logger.ReviewRebateTrace(string(encoded))
}

// reviewRebateTraceWasSanitized 判断一段已序列化的 trace 文本会被脱敏改写。
//
// 用 logger 的同一个 RedactSecrets 判定，保证「日志里到底被改没改写」
// 与 payload 里的 sanitized 位是**同一个事实**，不会出现
// 「sanitized=false 但日志里密钥已被抹掉」这种自相矛盾。
func reviewRebateTraceWasSanitized(text string) bool {
	return logger.RedactSecrets(text) != text
}

// reviewRebateTraceTimestamp 把发生时刻格式化成与日志行首一致的形式。
//
// 零值（调用方没设 StartedAt）退回当前时刻并返回——不 panic、
// 不写 "0001-01-01"，因为一条 ts 为空的时间序列比一条近似时间更难用。
func reviewRebateTraceTimestamp(startedAt time.Time) string {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return startedAt.In(logger.ShanghaiLocation()).Format("2006/01/02 - 15:04:05")
}

// reviewRebatePromptDigest 返回提示词的 sha256 前 16 位十六进制。
//
// 只取前 16 位：完整 64 位在日志里是噪音，16 位足以区分版本。
func reviewRebatePromptDigest(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])[:16]
}
