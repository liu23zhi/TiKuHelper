package util

import (
	"github.com/antlabs/strsim"
	"github.com/antlabs/strsim/similarity"
	"github.com/gookit/goutil/arrutil"
	"github.com/itihey/tikuAdapter/pkg/model"
	"regexp"
	"strings"
)

var sep = "**=====^_^======^_^======**" // 用于分割答案的分隔符

// FillAnswerResponse 根据搜题结果填充答案
func FillAnswerResponse(answers [][]string, req *model.SearchRequest) model.SearchResponse {

	req.Question = FormatString(req.Question)
	req.Options = FormatOptions(req.Options, req.Type)

	// 归一化所有答案，以便匹配
	normalizedAnswers := normalizeAnswers(answers)

	resp := model.SearchResponse{
		Question: req.Question,
		Options:  req.Options,
		Type:     req.Type,
		Answer: model.Answer{
			AllAnswer:   answers, // 保留原始答案用于展示
			AnswerIndex: []int{},
			AnswerKey:   []string{},
			BestAnswer:  []string{},
		},
	}

	// 非客观题（2 填空/连线、4 简答、-4 阅读理解，以及其它自由文本题型）的答案正文
	// 是用户要填进空里的原文，**不能**被 FormatString 改写标点或全角字符：
	// 把「这是答案。」改成「这是答案」会让填进去的内容与被考答案不一致。
	//
	// 参考 chaoxingexam/subjective_answers.py：:13 的 SUBJECTIVE_TYPES 明确包含
	// 「填空题」；:136-149 的 clean_html_tags 只清 HTML 标签、保留标点不做归一化。
	//
	// 归一化仍然参与「哪些组是同一个答案」的判定，只是不参与输出。
	if !isObjectiveQuestionType(resp.Type) {
		resp.Answer.BestAnswer = searchRightAnswerPreservingText(answers)
		fillAnswer(&resp.Answer, req)
		return resp
	}

	if req.Options == nil || len(req.Options) == 0 { // 用户没有传选项，那么只能返回出现次数最多的答案。
		resp.Answer.BestAnswer = SearchRightAnswer(normalizedAnswers)
	} else {
		var filterAnswer [][]string
		// Prefer complete exact groups. Only fall back to fuzzy mapping when no
		// exact group is valid; never turn a partially matched group into an answer.
		for _, allowFuzzy := range []bool{false, true} {
			for _, group := range normalizedAnswers {
				if mapped := matchObjectiveAnswerGroup(group, req, allowFuzzy); len(mapped) > 0 {
					filterAnswer = append(filterAnswer, mapped)
				}
			}
			if len(filterAnswer) > 0 {
				break
			}
		}
		resp.Answer.BestAnswer = SearchRightAnswer(filterAnswer)
	}
	fillAnswer(&resp.Answer, req)
	return resp
}

func isObjectiveQuestionType(questionType int) bool {
	return questionType == 0 || questionType == 1 || questionType == 3
}

// IsObjectiveQuestionType 暴露客观题判定，供 AI 解析与兜底链路复用，
// 避免各处各写一份 type 判断而彼此漂移。
//
// 题型语义（见 controller 的题型标签）：0 单选、1 多选、2 填空题/连线题、
// 3 判断、4 简答题/阅读理解、-4 阅读理解。
func IsObjectiveQuestionType(questionType int) bool {
	return isObjectiveQuestionType(questionType)
}

// AllowsRepeatedAnswers 判断该题型的答案是否允许重复。
//
// 填空题 / 连线题 / 简答题 / 阅读理解（2、4、-4）是**非客观题**：
// 多个空、多组连线、多个要点可能都是同一个答案文本，重复必须原样保留，
// 否则空格数与答案数对不上，前端按序填答就会错位。
//
// 客观题（单选 / 多选 / 判断）的答案来自选项集合，重复没有意义，可以去重。
func AllowsRepeatedAnswers(questionType int) bool {
	return !isObjectiveQuestionType(questionType)
}

func fillAnswer(a *model.Answer, req *model.SearchRequest) {
	if len(a.BestAnswer) == 0 {
		a.BestAnswer = make([]string, 0)
	}

	if len(a.AllAnswer) == 0 {
		a.AllAnswer = make([][]string, 0)
	}

	a.AnswerIndex = findIndices(a.BestAnswer, req.Options)
	a.AnswerText = strings.Join(a.BestAnswer, "#")

	a.AnswerKey = make([]string, len(a.AnswerIndex))

	for i, index := range a.AnswerIndex {
		a.AnswerKey[i] = string(rune(index + 65))
	}
	a.AnswerKeyText = arrutil.JoinStrings("", a.AnswerKey...)
}

// SearchRightAnswer 此方法还有巨大的优化空间
func SearchRightAnswer(answers [][]string) []string {
	answerMap := make(map[string]int)
	for _, answer := range answers {
		sortedAnswer := strings.Join(answer, sep)
		answerMap[sortedAnswer]++
	}
	maxCount := 0
	for _, count := range answerMap {
		if count > maxCount {
			maxCount = count
		}
	}
	for _, answer := range answers {
		if answerMap[strings.Join(answer, sep)] == maxCount {
			return append([]string(nil), answer...)
		}
	}
	return nil
}

// searchRightAnswerPreservingText 与 SearchRightAnswer 的「取出现次数最多的那一组」
// 语义完全一致，但**返回原始答案组成员**，不返回归一化后的副本。
//
// 用于非客观题（填空/连线/简答/阅读理解）：这些题的答案是用户要填进空里的原文，
// 归一化只应参与「哪几组算同一个答案」的判定，不应改写输出正文。
// 例如「因为：所以」与「因为:所以」应被视为同一组（归一键相同），
// 但返回给客户端的必须是排在最前面的那条原始文本。
//
// 归一化口径与 normalizeAnswers 一致（都走 FormatString），
// 否则同一组在两条路径上会被判成不同答案。
func searchRightAnswerPreservingText(answers [][]string) []string {
	if len(answers) == 0 {
		return nil
	}
	normalizedKeys := make([]string, len(answers))
	counts := make(map[string]int, len(answers))
	for i, answer := range answers {
		key := strings.Join(normalizeAnswerSet(answer), sep)
		normalizedKeys[i] = key
		counts[key]++
	}
	maxCount := 0
	for _, count := range counts {
		if count > maxCount {
			maxCount = count
		}
	}
	// 与 SearchRightAnswer 一致：并列时取**最先出现**的那一组，保证结果确定。
	for i, answer := range answers {
		if counts[normalizedKeys[i]] == maxCount {
			return append([]string(nil), answer...)
		}
	}
	return nil
}

// normalizeAnswerSet 对单个答案组逐个元素做 FormatString，
// 与 normalizeAnswers 使用完全相同的口径。
func normalizeAnswerSet(answerSet []string) []string {
	normalized := make([]string, len(answerSet))
	for i, ans := range answerSet {
		normalized[i] = FormatString(ans)
	}
	return normalized
}

const (
	fuzzyAnswerMinScore  = 0.7
	fuzzyAnswerMinMargin = 0.1
)

// answerLabelPrefix matches the option label a provider sometimes repeats at the
// head of the answer text ("B. 还原反应", "C、38%"). FormatOptions already drops
// this prefix from options, so the answer side needs the same treatment before
// comparing, otherwise a correctly labelled answer never matches its option.
var answerLabelPrefix = regexp.MustCompile(`^[A-Za-z][.．、:：)）]\s*`)

func stripAnswerLabelPrefix(answer string) string {
	trimmed := answerLabelPrefix.ReplaceAllString(answer, "")
	if strings.TrimSpace(trimmed) == "" {
		return answer
	}
	return trimmed
}

func matchObjectiveAnswerGroup(answers []string, req *model.SearchRequest, allowFuzzy bool) []string {
	if len(answers) == 0 {
		return nil
	}
	selected := make(map[int]bool)
	for _, rawAnswer := range answers {
		// 统一清洗：全角转半角、「答案：」类前缀、选项标签前缀、首尾分隔符。
		// 旧实现只有 stripAnswerLabelPrefix 一道，处理不了 "答案：还原反应"
		// 与 "【答案】A" 这类形态，会让本来有答案的题返回空。详见 answer_token.go。
		answer := normalizeAnswerToken(rawAnswer)
		// cleanedAnswer 保留「标签前缀」，只做全角/「答案：」/首尾分隔符清洗。
		// 多标号判定必须用它：`A、D` 若先剥标签前缀会退化成 `D`（少选一个），
		// 那是静默错答。详见 answer_token.go 里 answerLabelIndexes 的注释。
		cleanedAnswer := cleanAnswerToken(rawAnswer)
		if req.Type == 3 {
			if polarity, ok := JudgementPolarity(answer); ok {
				matchedIndex, matchedCount := -1, 0
				for i, option := range req.Options {
					if optionPolarity, valid := JudgementPolarity(option); valid && optionPolarity == polarity {
						matchedIndex, matchedCount = i, matchedCount+1
					}
				}
				if matchedCount > 1 {
					return nil
				}
				if matchedCount == 1 {
					selected[matchedIndex] = true
					continue
				}
			}
		}
		if strings.TrimSpace(answer) == "" {
			return nil
		}
		index, count := -1, 0
		for i, option := range req.Options {
			if option == answer {
				index, count = i, count+1
			}
		}
		if count > 1 {
			return nil
		}
		if count == 0 {
			// 判断题的选项常常就是 A/B 两值（题库普遍用 A=正确、B=错误），
			// 纯字母答案必须能映射到对应选项。依据见 chaoxing-exam 的
			// references/answer-json-format.md:14 与 SKILL.md:272-278 陷阱 4：
			// 字母是客户端填答与题库存储的常见形态，服务端必须能解析。
			//
			// 三个必要条件（缺一不可，见 captain 2026-11-10 裁定）：
			//  1. 题型必须是 type=3（判断题）。单选题的 "B" 既可能是选项标号，
			//     也可能是某个选项正文本身就是 "B"，无法区分；既有测试
			//     （letter-only answer stays unmapped）有意钉住「不映射」。
			//  2. **选项数必须恰好是 2**。判断题结构性只有两个选项；超过 2 个
			//     说明这不是标准判断题（题库脏数据 / 题型标错），此时把字母当
			//     标号会命中一个语义上不存在的选项 —— 例如 4 选项的 "C" 会被
			//     映射成第 3 项，而该题根本没有「对/错」可选项。
			//  3. 字面优先：上面的 count==0 已保证「没有字面等于该字母的选项」。
			if req.Type == 3 && len(req.Options) == 2 {
				if labelIndex, ok := optionLabelIndex(answer, len(req.Options)); ok {
					selected[labelIndex] = true
					continue
				}
			}
			// 多选题的字母答案扩展到多标号形态（"A,B" / "A、B" / "ABD"）。
			// 依据 chaoxing-exam/SKILL.md:272-278 陷阱 4：客户端填写时必须用
			// 「显示字母」匹配选项，题库存储的也常是字母串。
			//
			// 只放开 type=1（多选）：
			//   - type=0（单选）保持「单个纯字母不映射」的既有边界（见上）；
			//   - type=3（判断）只有两个选项，多标号在该题型上是脏数据，不猜。
			if req.Type == 1 {
				if labelIndexes, ok := answerLabelIndexes(cleanedAnswer, len(req.Options)); ok && len(labelIndexes) > 1 {
					for _, labelIndex := range labelIndexes {
						selected[labelIndex] = true
					}
					continue
				}
			}
			if !allowFuzzy {
				return nil
			}
			match := strsim.FindBestMatch(answer, req.Options)
			if match == nil || match.Match == nil || match.Match.Score < fuzzyAnswerMinScore || fuzzyAnswerMargin(match) < fuzzyAnswerMinMargin {
				return nil
			}
			index = match.BestIndex
		}
		selected[index] = true
	}
	if req.Type != 1 && len(selected) != 1 {
		return nil
	}
	var mapped []string
	for index, option := range req.Options {
		if selected[index] {
			mapped = append(mapped, option)
		}
	}
	return mapped
}

// optionLabelIndex 把「选项标号」形式的答案解析成 0 基选项下标。
//
// 只接受**单个字母**（A-Z / a-z），允许前后成对包裹的标点
// （"A"、"A."、"A、"、"(A)"、"（A）"、"[A]"、"【A】"）。
// 多字母或含其它内容一律不当作标号 —— 那是答案文本，不是标号，
// 硬猜会把 "AB" 这种答案误映射成选项 A。
//
// 越界（下标 >= optionCount）返回 false，由调用方按「无法映射」处理，
// 避免把越界标号当成某个真实选项（「唯一例外」：越界标号不得回落到任何真实选项）。
func optionLabelIndex(answer string, optionCount int) (int, bool) {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" || optionCount <= 0 {
		return 0, false
	}
	// 只剥一层成对标点，且剥完必须恰好剩一个字母。
	for _, pair := range [][2]string{
		{"(", ")"}, {"（", "）"}, {"[", "]"}, {"【", "】"},
	} {
		if strings.HasPrefix(trimmed, pair[0]) && strings.HasSuffix(trimmed, pair[1]) && len(trimmed) > len(pair[0])+len(pair[1]) {
			trimmed = strings.TrimSpace(trimmed[len(pair[0]) : len(trimmed)-len(pair[1])])
			break
		}
	}
	// 剥尾随的单点号/顿号：只剩一个字母时它们只是标号的装饰。
	trimmed = strings.TrimRight(trimmed, ".．、:：)")
	if trimmed == "" {
		return 0, false
	}
	runes := []rune(trimmed)
	if len(runes) != 1 {
		return 0, false
	}
	letter := runes[0]
	switch {
	case letter >= 'A' && letter <= 'Z':
		index := int(letter - 'A')
		if index >= optionCount {
			return 0, false
		}
		return index, true
	case letter >= 'a' && letter <= 'z':
		index := int(letter - 'a')
		if index >= optionCount {
			return 0, false
		}
		return index, true
	default:
		return 0, false
	}
}

func fuzzyAnswerMargin(match *similarity.MatchResult) float64 {
	secondBest := 0.0
	for index, candidate := range match.AllResult {
		if index == match.BestIndex || candidate == nil {
			continue
		}
		if candidate.Score > secondBest {
			secondBest = candidate.Score
		}
	}
	return match.Match.Score - secondBest
}

func getMaxCountAnswer(answerCount map[string]int) []string {
	maxCount := 0
	var correctAnswers []string

	for answer, count := range answerCount {
		newAnswers := strings.Split(answer, sep)
		if count > maxCount /*|| len(newAnswers) > len(correctAnswers)*/ {
			maxCount = count
			correctAnswers = newAnswers
		}
	}
	return correctAnswers
}

func findIndices(answers []string, options []string) []int {
	indices := make([]int, 0)
	for _, answer := range answers {
		for i, option := range options {
			if option == answer {
				indices = append(indices, i)
				break
			}
		}
	}
	return indices
}

// normalizeAnswers 归一化所有答案，便于匹配
func normalizeAnswers(answers [][]string) [][]string {
	normalized := make([][]string, len(answers))
	for i, answerSet := range answers {
		normalized[i] = make([]string, len(answerSet))
		for j, ans := range answerSet {
			normalized[i][j] = FormatString(ans)
		}
	}
	return normalized
}
