package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

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
		// attempt：同一 claim 的第几次打分，用于区分重跑先后（见 reviewRebateTraceRecord）。
		"attempt": record.Attempt,
		// 系统提示词**全文** + sha256：全文供复盘提示词是否被带偏，
		// sha256 供快速比对「换没换版本」（16 位足够）。
		"systemPrompt":       systemPrompt,
		"systemPromptSha256": reviewRebatePromptDigest(systemPrompt),
		"durationMs":         record.DurationMs,
		"claimedStars":       record.Claim.ClaimedStars,
		"claimedReviewUrl":   strings.TrimSpace(record.Claim.ClaimedReviewURL),
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

	encoded, err := json.Marshal(payload)
	if err != nil {
		// 序列化失败不该发生（全是基础类型），真发生了也只记一条简短说明。
		logger.SysWarn("评价返现 trace 序列化失败: " + err.Error())
		return
	}
	logger.ReviewRebateTrace(string(encoded))
}

// reviewRebatePromptDigest 返回提示词的 sha256 前 16 位十六进制。
//
// 只取前 16 位：完整 64 位在日志里是噪音，16 位足以区分版本。
func reviewRebatePromptDigest(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])[:16]
}
