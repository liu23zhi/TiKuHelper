package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R17 · answer_pattern_repeat（97 §5.1 L326）
//
// # ⚠️ 与 97 号原文存在**语义偏差**，本文件必须记明
//
// 97 号原话：
//
//	R17 | answer_pattern_repeat | 同一用户对同一题重复**提交** ≥20 次（刷题库特征）
//	    | 数据源：**答题日志** | ✅ 封号 1 天
//
// 两处与本仓实际不符，实测依据如下：
//
//  1. **「答题日志」这张表不存在**。本仓没有任何「谁在何时答了哪道题」的表
//     （97 号 L341 自己也把「答题日志」列在「需确认表名与字段」的待确认项里）。
//
//  2. **实际数据源 `billing_records.question_text` 记的是「搜题」不是「提交答案」**。
//     实测 `billing_records.type` 的取值分布：
//
//	normal_search       51 行   ← 搜题计费
//	recharge             1 行
//	balance_adjustment   1 行
//
//     所以本规则的真实语义是「同一用户反复**搜**同一道题」。
//     它同时覆盖两种情形：正常用户反复核对答案、脚本批量抓题库。
//     **不能假装它等价于「刷题库」** —— 前者可能是正常行为，后者才是滥用。
//
// 这个偏差直接决定阈值的意义：97 号的「20 次提交」偏滥用，
// 而「20 次搜索」更像是「同一个人把一道题核对了 20 遍」，本身可疑但不定罪。
// 因此本实现保留 20（不自行调低），并在 Description 里写明运营可上调。
//
// # 判据
//
// 同一 user_id + 同一归一化题干，在 7 天滚动窗口内 ≥N 次（N 默认 20）。
//
// # 窗口为什么是 7 天（97 号未给）
//
// 搜题是低频行为（一个人一天搜几十道题很正常，但同一道题很少重复）。
// 「同一道题累计 20 次」不是短时爆发，而是**长期累积**特征：
//   - 24h 内同题 20 次 → 门槛太高，正常脚本一天内未必重复到
//   - 30 天 → 跨度过大，把「一学期里复习时反复看同一题」也算进来
//   - **7 天**是折中：足够覆盖一次集中的题库抓取，又不会把长期复习算进来
//
// ════════════════════════════════════════════════════════════════════

// riskAnswerRepeatWindow 是 R17 的判定窗口（7 天**滚动**，97 号未给，本仓自定）。
const riskAnswerRepeatWindow = 7 * 24 * time.Hour

// riskAnswerRepeatMinQuestionRunes 是参与判定所需的**最小题干长度**（归一化后，按 rune 计）。
//
// 为什么要有这个下限：短题干会天然误合 ——「1+1」「A」「判断题」这类文本
// 在不同题目之间大量重复，按它们聚合会把「不同题的多次搜索」算成「同一题」。
// 8 个字符足以排除绝大多数无信息量的题干（一个完整的中文题目通常远超此长度）。
//
// ⚠️ 8 是本仓自定的经验值，97 号未提及。
const riskAnswerRepeatMinQuestionRunes = 8

// riskAnswerRepeatSnippetRunes 是 detail 里题干摘要的最大长度（rune 计）。
//
// 60 个字符足以让管理员认出「他在刷哪道题」，又不会让 detail JSON 过大。
const riskAnswerRepeatSnippetRunes = 60

// normalizeAnswerRepeatQuestion 归一化题干，用于**精确匹配**。
//
// 只做两件事：去首尾空白、把连续的空白（含全角空格/制表/换行）折叠成单个半角空格。
//
// # 为什么不做 hash
//
// 存量 `question_text` 没有 hash 列，加 hash 需要回填迁移；
// 而 `question_text` 有索引，按归一化后的文本匹配可以直接在 Go 侧分组，
// 数据量级（账单行）也可控。
//
// # 为什么不做模糊匹配
//
// 相似度匹配会把「同一章节的不同题目」误合（题干往往只差一个数字），
// 对 R17 这种「重复到 20 次」的判据来说，误合的代价是直接误报。
//
// ⚠️ 已知漏报面：用户从不同来源复制题干时可能差一个标点或错别字，
// 归一化只处理**空白**，处理不了标点差异。这类漏报如实写进 Description。
func normalizeAnswerRepeatQuestion(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	// 把各类空白（含全角空格 U+3000、制表、换行）折叠成单个半角空格。
	var builder strings.Builder
	builder.Grow(len(trimmed))
	previousSpace := false
	for _, r := range trimmed {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u3000'
		if isSpace {
			if !previousSpace {
				builder.WriteByte(' ')
			}
			previousSpace = true
			continue
		}
		builder.WriteRune(r)
		previousSpace = false
	}
	return strings.TrimSpace(builder.String())
}

// truncateAnswerRepeatQuestion 截取题干摘要（按 rune 计数，避免切断多字节字符）。
func truncateAnswerRepeatQuestion(question string, limit int) string {
	if utf8.RuneCountInString(question) <= limit {
		return question
	}
	runes := []rune(question)
	return string(runes[:limit]) + "…"
}

// collectAnswerPatternRepeatCandidates 收集 R17 的命中（97 §5.1）。
//
// # 边界（每条都有用例）
//
//   - **题干为空 → 跳过**（不能把「空题」当成同一道题）。
//   - **归一化后短于 8 字符 → 跳过**（短题干会天然误合不同题）。
//   - **不同用户搜同一题 → 不告警**（那是「热门题」，不是个人重复行为）。
//   - **同一用户搜不同题 → 不告警**。
//   - **只统计 `type = normal_search`** —— 余额调整/充值流水不是搜题。
//   - **`hidden_from_statement = true` 的行排除** —— 那是调整流水，
//     不是真实的搜题记录（该字段语义见 `entity/billing_record.go:37`）。
func collectAnswerPatternRepeatCandidates(db *gorm.DB, rule entity.RiskRule, now time.Time) ([]riskWarningCandidate, error) {
	threshold := riskRuleThreshold(db, rule, riskDefaultThreshold(rule.RuleID))
	if threshold <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameBillingRecord) {
		return nil, nil
	}
	windowStart, windowEnd := now.Add(-riskAnswerRepeatWindow), now
	timeExpr := riskWindowColumnExpr(db, "created_at")

	type repeatRow struct {
		UserID       int32
		QuestionText string
	}
	var rows []repeatRow
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Select("user_id, question_text").
		Where("type = ?", "normal_search").
		Where("hidden_from_statement = ?", false).
		Where("user_id > 0").
		Where("question_text <> ''").
		Where(timeExpr+" >= ? AND "+timeExpr+" < ?",
			windowStart.Format("2006-01-02 15:04:05"), windowEnd.Format("2006-01-02 15:04:05")).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 按 (userID, 归一化题干) 聚合。键用结构体而不是拼接字符串：
	// 题干里可能含任意分隔符，拼接会产生撞键（「a:b」+「c」与「a」+「b:c」同键）。
	type repeatKey struct {
		UserID      int32
		NormalizedQ string
	}
	type repeatGroup struct {
		Count    int
		Original string
	}
	groups := make(map[repeatKey]*repeatGroup, 8)
	order := make([]repeatKey, 0, 8)
	for _, row := range rows {
		normalized := normalizeAnswerRepeatQuestion(row.QuestionText)
		if normalized == "" {
			continue // 空题：不能当成同一道题
		}
		if utf8.RuneCountInString(normalized) < riskAnswerRepeatMinQuestionRunes {
			continue // 短题干：天然误合，跳过
		}
		key := repeatKey{UserID: row.UserID, NormalizedQ: normalized}
		group, ok := groups[key]
		if !ok {
			group = &repeatGroup{Original: strings.TrimSpace(row.QuestionText)}
			groups[key] = group
			order = append(order, key)
		}
		group.Count++
	}

	windowStartForSubject, _ := riskLoginWindow(now)
	candidates := make([]riskWarningCandidate, 0, 4)
	for _, key := range order {
		group := groups[key]
		// 「≥ 阈值」：低于阈值跳过。
		if float64(group.Count) < threshold {
			continue
		}
		userID := key.UserID
		// SubjectKey 不能放完整题干（可能很长、含任意字符）。
		// 用「用户 + 题干长度 + 归一化题干的前 32 字符」做一个可读且足够区分的标识。
		subject := riskBehaviorSubject(rule.RuleID,
			"user:"+strconv.FormatInt(int64(userID), 10)+
				":q:"+truncateAnswerRepeatQuestion(key.NormalizedQ, 32),
			windowStartForSubject)
		detail, err := json.Marshal(map[string]any{
			"ruleId":     rule.RuleID,
			"subjectKey": subject,
			"userId":     userID,
			"username":   riskUsernamesByID(db, []int32{userID})[userID],
			// threshold 是**配置值**，不是实际计数（后者在 repeatCount）。
			"threshold":       threshold,
			"value":           group.Count,
			"repeatCount":     group.Count,
			"questionSnippet": truncateAnswerRepeatQuestion(group.Original, riskAnswerRepeatSnippetRunes),
			"questionRunes":   utf8.RuneCountInString(key.NormalizedQ),
			"windowDays":      int(riskAnswerRepeatWindow / (24 * time.Hour)),
			"windowKind":      "rolling",
			"windowStart":     windowStart.Format(time.RFC3339),
			"windowEnd":       now.Format(time.RFC3339),
			"evaluatedAt":     now.Format(time.RFC3339),
			"evidenceNote": "同一用户在 7 天滑动窗口内对同一道题的**搜题**次数（billing_records.type=normal_search）。" +
				"注意：本规则统计的是「搜题」不是「提交答案」（本仓没有答题日志表）。" +
				"题干按「去首尾空白 + 折叠连续空白」归一化后精确匹配，不做模糊匹配；" +
				"空题干与归一化后短于 8 字符的题干不参与",
		})
		if err != nil {
			return nil, err
		}
		userIDCopy := userID
		candidates = append(candidates, riskWarningCandidate{
			Rule:     rule,
			Subject:  subject,
			UserID:   &userIDCopy,
			Severity: rule.Severity,
			Detail:   string(detail),
		})
	}
	return candidates, nil
}
