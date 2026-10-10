package util

import (
	"github.com/antlabs/strsim"
	"github.com/gookit/goutil/arrutil"
	"github.com/itihey/tikuAdapter/pkg/model"
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
		Plat:     req.Plat,
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
		// 使用归一化后的答案进行匹配
		for i := range normalizedAnswers {
			ans := arrutil.Intersects(req.Options, normalizedAnswers[i], arrutil.StringEqualsComparer)
			if ((resp.Type == 0 || resp.Type == 3) && len(ans) > 0) || (resp.Type == 1 && len(ans) > 1) {
				filterAnswer = append(filterAnswer, ans)
			}
		}
		resp.Answer.BestAnswer = SearchRightAnswer(filterAnswer)

		if len(resp.Answer.BestAnswer) == 0 { // 开始模糊匹配
			for i := range normalizedAnswers {
				if resp.Type == 0 { // 单选或判断题
					match := strsim.FindBestMatch(strings.Join(normalizedAnswers[i], ""), req.Options)
					filterAnswer = append(filterAnswer, []string{resp.Options[match.BestIndex]})
				} else {
					ans := arrutil.Intersects(req.Options, normalizedAnswers[i], func(a, b string) int {
						if strsim.Compare(a, b) >= 0.7 {
							return 0
						}
						return -1
					})
					if len(ans) > resp.Type {
						filterAnswer = append(filterAnswer, ans)
					}
				}
				resp.Answer.BestAnswer = SearchRightAnswer(filterAnswer)
			}
		}
	}
	fillAnswer(&resp.Answer, req)
	return resp
}

func isObjectiveQuestionType(questionType int) bool {
	return questionType == 0 || questionType == 1 || questionType == 3
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
	return getMaxCountAnswer(answerMap)
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
