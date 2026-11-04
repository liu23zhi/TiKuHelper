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

	// 文本题的一个答案组可能包含多个小问，必须保留整组答案和原始文本。
	// 只有一个答案组时没有聚合歧义，直接返回该组，避免格式化过程改写正文。
	if (req.Type == 4 || req.Type == -4) && len(answers) == 1 {
		resp.Answer.BestAnswer = append([]string(nil), answers[0]...)
		fillAnswer(&resp.Answer, req)
		return resp
	}

	if req.Options == nil || len(req.Options) == 0 { // 用户没有传选项，那么只能返回出现次数最多的答案。
		resp.Answer.BestAnswer = SearchRightAnswer(normalizedAnswers)
	} else if !isObjectiveQuestionType(resp.Type) {
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
		answer := stripAnswerLabelPrefix(rawAnswer)
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
