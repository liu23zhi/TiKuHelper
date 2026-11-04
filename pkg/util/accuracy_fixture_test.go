package util

import (
	"os"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// Labeled fixture benchmark for objective-answer mapping.
//
// It measures how often FillAnswerResponse lands on the ground-truth option
// letters, how often it answers wrongly, and how often it abstains. The same
// file can be copied into a worktree of an older commit to produce a baseline.
//
// Run with: ACCURACY_BENCH=1 go test ./pkg/util/ -run TestObjectiveAnswerAccuracyFixtures -v
//
// Ground truth is expressed as the expected AnswerKey string ("A", "AD") or ""
// when no option can be justified from the provider answers.

type accuracyFixture struct {
	id      string
	kind    string // single | multiple | judgement
	qtype   int
	options []string
	answers [][]string
	expect  string
	note    string
}

var accuracyFixtures = []accuracyFixture{
	{
		id: "single-exact", kind: "single", qtype: 0,
		options: []string{"光合作用", "呼吸作用"},
		answers: [][]string{{"光合作用"}},
		expect:  "A",
		note:    "Exact option text",
	},
	{
		id: "single-exact-later-option", kind: "single", qtype: 0,
		options: []string{"呼吸作用", "光合作用", "蒸腾作用"},
		answers: [][]string{{"光合作用"}},
		expect:  "B",
		note:    "Exact text on a non-first option",
	},
	{
		id: "single-label-prefixed-answer", kind: "single", qtype: 0,
		options: []string{"A. 氧化反应", "B. 还原反应"},
		answers: [][]string{{"B. 还原反应"}},
		expect:  "B",
		note:    "Answer repeats the option label",
	},
	{
		id: "single-full-width", kind: "single", qtype: 0,
		options: []string{"x+y=z", "x-y=z"},
		answers: [][]string{{"x＋y＝z"}},
		expect:  "A",
		note:    "Full-width operators normalize to the option text",
	},
	{
		id: "single-fuzzy-clear-winner", kind: "single", qtype: 0,
		options: []string{"细胞膜的流动性", "细胞壁的全透性"},
		answers: [][]string{{"细胞膜的流动性"}},
		expect:  "A",
		note:    "Exact winner among similar options",
	},
	{
		id: "single-ambiguous", kind: "single", qtype: 0,
		options: []string{"牛顿第一定律", "牛顿第二定律"},
		answers: [][]string{{"牛顿定律"}},
		expect:  "",
		note:    "Two options tie; any pick is a guess",
	},
	{
		id: "single-junk-answer", kind: "single", qtype: 0,
		options: []string{"甲方案", "乙方案"},
		answers: [][]string{{"完全无关的垃圾文本"}},
		expect:  "",
		note:    "Unrelated provider answer must not be mapped",
	},
	{
		id: "single-duplicate-options", kind: "single", qtype: 0,
		options: []string{"同一文本", "同一文本"},
		answers: [][]string{{"同一文本"}},
		expect:  "",
		note:    "Duplicate option text is unmappable",
	},
	{
		id: "single-empty-answer", kind: "single", qtype: 0,
		options: []string{"甲", "乙"},
		answers: [][]string{{""}},
		expect:  "",
		note:    "Empty answer cannot be mapped",
	},
	{
		id: "single-partial-group", kind: "single", qtype: 0,
		options: []string{"甲", "乙", "丙"},
		answers: [][]string{{"甲", "丁"}},
		expect:  "",
		note:    "One unmappable member invalidates the group",
	},
	{
		id: "single-vote-tie-deterministic", kind: "single", qtype: 0,
		options: []string{"甲", "乙"},
		answers: [][]string{{"甲"}, {"乙"}},
		expect:  "A",
		note:    "Tie resolves deterministically to the first provider group",
	},
	{
		id: "single-two-groups-agree", kind: "single", qtype: 0,
		options: []string{"甲", "乙", "丙"},
		answers: [][]string{{"乙"}, {"乙"}, {"丙"}},
		expect:  "B",
		note:    "Majority agreement wins",
	},
	{
		id: "multiple-two-answers", kind: "multiple", qtype: 1,
		options: []string{"氧化", "还原", "中和", "沉淀"},
		answers: [][]string{{"氧化", "沉淀"}},
		expect:  "AD",
		note:    "Two exact answers",
	},
	{
		id: "multiple-order-irrelevant", kind: "multiple", qtype: 1,
		options: []string{"氧化", "还原", "中和"},
		answers: [][]string{{"中和", "氧化"}},
		expect:  "AC",
		note:    "Answer order must not change the key order",
	},
	{
		id: "multiple-partial-group", kind: "multiple", qtype: 1,
		options: []string{"甲", "乙", "丙"},
		answers: [][]string{{"甲", "丙", "丁"}},
		expect:  "",
		note:    "Partial multi-answer group is unsafe",
	},
	{
		id: "multiple-single-answer-valid", kind: "multiple", qtype: 1,
		options: []string{"甲", "乙", "丙"},
		answers: [][]string{{"乙"}},
		expect:  "B",
		note:    "A multi-select question can legitimately have one answer",
	},
	{
		id: "judgement-cross", kind: "judgement", qtype: 3,
		options: []string{"正确", "错误"},
		answers: [][]string{{"√"}},
		expect:  "A",
		note:    "Polarity symbol maps to the option text",
	},
	{
		id: "judgement-reversed", kind: "judgement", qtype: 3,
		options: []string{"错误", "正确"},
		answers: [][]string{{"对"}},
		expect:  "B",
		note:    "Polarity must follow option position, not assume A=正确",
	},
	{
		id: "judgement-english", kind: "judgement", qtype: 3,
		options: []string{"正确", "错误"},
		answers: [][]string{{"false"}},
		expect:  "B",
		note:    "English polarity words",
	},
	{
		id: "judgement-author-format", kind: "judgement", qtype: 3,
		options: []string{"错", "对"},
		answers: [][]string{{"是"}},
		expect:  "B",
		note:    "Author writes 错/对 instead of 正确/错误",
	},
	{
		id: "judgement-junk", kind: "judgement", qtype: 3,
		options: []string{"正确", "错误"},
		answers: [][]string{{"大概吧"}},
		expect:  "",
		note:    "Non-polarity answer must not be mapped",
	},
}

func TestObjectiveAnswerAccuracyFixtures(t *testing.T) {
	if os.Getenv("ACCURACY_BENCH") != "1" {
		t.Skip("set ACCURACY_BENCH=1 to run the labeled accuracy benchmark")
	}

	var correct, wrong, abstained int
	var answerable, answerableCorrect int

	for _, fixture := range accuracyFixtures {
		req := &model.SearchRequest{
			Question: "fixture",
			Options:  append([]string(nil), fixture.options...),
			Type:     fixture.qtype,
		}
		resp := FillAnswerResponse(fixture.answers, req)
		got := resp.Answer.AnswerKeyText
		if fixture.expect != "" {
			answerable++
		}
		verdict := "wrong"
		switch {
		case got == fixture.expect:
			verdict = "correct"
			correct++
			if fixture.expect != "" {
				answerableCorrect++
			}
		case got == "" && fixture.expect != "":
			verdict = "abstained"
			abstained++
		default:
			wrong++
		}
		t.Logf("%-28s expected=%-3q got=%-3q %s  (%s)", fixture.id, fixture.expect, got, verdict, fixture.note)
	}

	total := len(accuracyFixtures)
	ambiguous := total - answerable
	t.Logf("SETTINGS total=%d answerable=%d ambiguous=%d", total, answerable, ambiguous)
	t.Logf("RESULT correct=%d wrong=%d abstained=%d answerable_correct=%d/%d accuracy_answerable=%.3f wrong_rate=%.3f",
		correct, wrong, abstained, answerableCorrect, answerable,
		float64(answerableCorrect)/float64(answerable),
		float64(wrong)/float64(total))
}
