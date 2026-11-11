package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

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
	Claim       entity.ReviewRebateClaim
	Scorer      string
	Model       string
	DurationMs  int64
	UserPrompt  string
	RawResponse string
	Parsed      *ReviewRebateScore
	// Outcome 取值：ok / error / parse_error / no_screenshot。
	Outcome string
	Error   string
}

// traceReviewRebateScoring 把一条打分记录序列化成 JSON Lines 写进独立日志。
//
// JSON Lines（一行一条）而不是 pretty JSON：便于 grep、便于按行切分解析，
// 且几十万行也不会让文件无法处理。
//
// 本函数**不得**返回错误或 panic —— 追踪失败绝不能影响打分主流程。
func traceReviewRebateScoring(record reviewRebateTraceRecord) {
	payload := map[string]any{
		"claimId":   record.Claim.ID,
		"userId":    record.Claim.UserID,
		"programId": record.Claim.ProgramID,
		"scorer":    record.Scorer,
		"model":     record.Model,
		"outcome":   record.Outcome,
		// 系统提示词只记 sha256，不记全文：它是固定资产，全文重复写入纯浪费，
		// 而 sha256 足以判断「版本变没变」。
		"systemPromptSha256": reviewRebatePromptDigest(ReviewRebateAgentSystemPrompt),
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
