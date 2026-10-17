package search

import (
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// 模型偶尔不按 {"answers":[...]} 返回；这些形态线上都真实出现过，
// 只认 answers 会整段当答案 → 客观题映射失败 → NO_ANSWER。
func TestParseAISearchAnswersAcceptsLooseModelShapes(t *testing.T) {
	options := []string{"正确", "错误"}
	cases := map[string]string{
		`{"answers":["正确"]}`:                   "正确",
		`{"answers":"正确"}`:                     "正确",
		`{"answer":"A","explanation":"正确。"}`:   "正确",
		`{"答案":"A","判断":"正确"}`:                 "正确",
		`{"answer":"A","result":"正确"}`:         "正确",
		"```json\n{\"answers\":[\"正确\"]}\n```": "正确",
	}
	for content, want := range cases {
		got := parseAISearchAnswers(content, options, 3)
		if !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("content=%s: expected [%s], got %v", content, want, got)
		}
	}
}

// 多选题：数组里每个元素是一个被选中的选项，必须原样保留（不能当成多个答案组）。
func TestParseAISearchAnswersKeepsAllMultiSelectOptions(t *testing.T) {
	options := []string{"甲", "乙", "丙", "丁"}
	content := `{"answers":["A. 甲","B. 乙","C. 丙"]}`
	got := parseAISearchAnswers(content, options, 1)
	want := []string{"甲", "乙", "丙"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// 线上事故：判断题选项带 "A. " 前缀时，模型返回「对」导致 bestAnswerCount=0
// （211 次判断题里 181 次空答案）。选项必须先归一化再做极性匹配。
func TestParseAISearchAnswersMapsJudgementAnswersWithPrefixedOptions(t *testing.T) {
	options := []string{"A. 正确", "B. 错误"}
	cases := map[string]string{
		"对":     "正确",
		"正确":    "正确",
		"√":     "正确",
		"正确。":   "正确",
		"错":     "错误",
		"×":     "错误",
		"B. 错误": "错误",
	}
	for answer, want := range cases {
		got := parseAISearchAnswers(`{"answers":["`+answer+`"]}`, options, 3)
		if !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("answer %q: expected [%s], got %v", answer, want, got)
		}
	}
}

// 端到端：解析结果交给 FillAnswerResponse 后必须得到非空答案。
func TestParseAndFillJudgementAnswerWithPrefixedOptions(t *testing.T) {
	req := model.SearchRequest{Options: []string{"A. 正确", "B. 错误"}, Type: 3}
	answers := parseAISearchAnswers(`{"answers":["对"]}`, req.Options, req.Type)
	resp := util.FillAnswerResponse([][]string{answers}, &req)
	if resp.Answer.AnswerText != "正确" || resp.Answer.AnswerKeyText != "A" {
		t.Fatalf("expected 正确/A, got %+v", resp.Answer)
	}
}
