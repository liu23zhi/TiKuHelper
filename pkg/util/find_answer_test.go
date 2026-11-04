package util

import (
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestFillAnswerResponsePreservesTextAnswersForNonObjectiveTypesWithOptions(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"左侧术语对应右侧案例"}}, &model.SearchRequest{
		Question: "请完成连线",
		Options:  []string{"选项一", "选项二"},
		Type:     15,
	})

	expected := []string{"左侧术语对应右侧案例"}
	if !reflect.DeepEqual(resp.Answer.BestAnswer, expected) {
		t.Fatalf("expected best answer %v, got %v", expected, resp.Answer.BestAnswer)
	}
	if resp.Answer.AnswerText != "左侧术语对应右侧案例" {
		t.Fatalf("expected answer text to preserve raw text answer, got %q", resp.Answer.AnswerText)
	}
	if len(resp.Answer.AnswerKey) != 0 {
		t.Fatalf("expected no answer key for non-objective text answers, got %v", resp.Answer.AnswerKey)
	}
}

func TestFillAnswerResponsePreservesAnswerTextForReaderStyleTypes(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"先分析材料，再给出结论"}}, &model.SearchRequest{
		Question: "请阅读材料后作答",
		Options:  []string{"材料一", "材料二"},
		Type:     4,
	})

	expected := []string{"先分析材料，再给出结论"}
	if !reflect.DeepEqual(resp.Answer.BestAnswer, expected) {
		t.Fatalf("expected best answer %v, got %v", expected, resp.Answer.BestAnswer)
	}
	if resp.Answer.AnswerText != "先分析材料，再给出结论" {
		t.Fatalf("expected answer text to preserve reader style text answer, got %q", resp.Answer.AnswerText)
	}
	if len(resp.Answer.AnswerKey) != 0 {
		t.Fatalf("expected no answer key for reader style text answers, got %v", resp.Answer.AnswerKey)
	}
}

func TestFillAnswerResponsePreservesAllTextAnswersForMultiPartQuestions(t *testing.T) {
	answers := [][]string{{"第一问答案", "第二问答案"}}
	resp := FillAnswerResponse(answers, &model.SearchRequest{
		Question: "请分别回答以下两个小问",
		Type:     4,
	})

	if !reflect.DeepEqual(resp.Answer.BestAnswer, answers[0]) {
		t.Fatalf("expected all text answers %v, got %v", answers[0], resp.Answer.BestAnswer)
	}
	if !reflect.DeepEqual(resp.Answer.AllAnswer, answers) {
		t.Fatalf("expected all answer groups %v, got %v", answers, resp.Answer.AllAnswer)
	}
	if resp.Answer.AnswerText != "第一问答案#第二问答案" {
		t.Fatalf("expected ordered multipart answer text, got %q", resp.Answer.AnswerText)
	}
}

func TestFillAnswerResponseRejectsLowConfidenceSingleChoiceFuzzyMatch(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"完全无关的回答"}}, &model.SearchRequest{
		Question: "请选择正确选项",
		Options:  []string{"苹果汁", "苹果树"},
		Type:     0,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("expected low-confidence answer to be rejected, got %v", resp.Answer.BestAnswer)
	}
}

func TestFillAnswerResponseRejectsAmbiguousSingleChoiceFuzzyMatch(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"苹果"}}, &model.SearchRequest{
		Question: "请选择正确选项",
		Options:  []string{"苹果汁", "苹果树"},
		Type:     0,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("expected ambiguous answer to be rejected, got %v", resp.Answer.BestAnswer)
	}
}

func TestFillAnswerResponseRejectsAmbiguousJudgementFuzzyMatch(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"alphabeta00"}}, &model.SearchRequest{
		Question: "错误标记成判断的单选题",
		Options:  []string{"alphabeta0x", "alphabeta0y"},
		Type:     3,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("expected ambiguous judgement answer to be rejected, got %v", resp.Answer.BestAnswer)
	}
}

func TestSearchRightAnswerUsesFirstGroupForTies(t *testing.T) {
	answers := [][]string{{"甲"}, {"乙"}}
	for i := 0; i < 20; i++ {
		got := SearchRightAnswer(answers)
		if !reflect.DeepEqual(got, []string{"甲"}) {
			t.Fatalf("expected deterministic first group on tie, got %v", got)
		}
	}
}

func TestFillAnswerResponseMapsJudgementPolarityToActualOptions(t *testing.T) {
	for _, tt := range []struct {
		name    string
		options []string
		answer  string
		want    []string
		index   int
	}{
		{"yes exact", []string{"yes", "no"}, "yes", []string{"yes"}, 0},
		{"no exact", []string{"yes", "no"}, "no", []string{"no"}, 1},
		{"uppercase true", []string{"TRUE", "FALSE"}, "TRUE", []string{"TRUE"}, 0},
		{"uppercase false synonym", []string{"TRUE", "FALSE"}, "错误", []string{"FALSE"}, 1},
		{"yes synonym", []string{"yes", "no"}, "正确", []string{"yes"}, 0},
		{"ambiguous polarity", []string{"yes", "TRUE"}, "yes", nil, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{Type: 3, Options: tt.options})
			if len(resp.Answer.BestAnswer) != len(tt.want) || (len(tt.want) > 0 && !reflect.DeepEqual(resp.Answer.BestAnswer, tt.want)) {
				t.Fatalf("want %v, got %v", tt.want, resp.Answer.BestAnswer)
			}
			if tt.index >= 0 && !reflect.DeepEqual(resp.Answer.AnswerIndex, []int{tt.index}) {
				t.Fatalf("want option index %d, got %v", tt.index, resp.Answer.AnswerIndex)
			}
		})
	}
}

func TestFillAnswerResponseStripsOptionLabelRepeatedByProvider(t *testing.T) {
	// 提供方常把选项标签重复进答案正文（"B. 还原反应"）。选项侧在 FormatOptions
	// 里已经剥过前缀，答案侧不剥就会把本来正确的答案判成无答案。
	for _, tt := range []struct {
		name    string
		answer  string
		options []string
		want    []string
		index   int
	}{
		{"dot label", "B. 还原反应", []string{"A. 氧化反应", "B. 还原反应"}, []string{"还原反应"}, 1},
		{"ideographic comma label", "C、38%", []string{"A、12%", "B、25%", "C、38%"}, []string{"38%"}, 2},
		{"colon label", "A：细胞膜", []string{"A：细胞膜", "B：细胞壁"}, []string{"细胞膜"}, 0},
		{"letter-only answer stays unmapped", "B", []string{"甲", "乙"}, nil, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{Type: 0, Options: tt.options})
			if len(resp.Answer.BestAnswer) != len(tt.want) || (len(tt.want) > 0 && !reflect.DeepEqual(resp.Answer.BestAnswer, tt.want)) {
				t.Fatalf("want %v, got %v", tt.want, resp.Answer.BestAnswer)
			}
			if tt.index >= 0 && !reflect.DeepEqual(resp.Answer.AnswerIndex, []int{tt.index}) {
				t.Fatalf("want option index %d, got %v", tt.index, resp.Answer.AnswerIndex)
			}
		})
	}
}

func TestFillAnswerResponseRejectsAmbiguousMultipleChoiceFuzzyMatch(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"alphabeta00"}}, &model.SearchRequest{
		Options: []string{"alphabeta0x", "alphabeta0y"}, Type: 1,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("ambiguous multi-choice answer must be rejected, got %v", resp.Answer.BestAnswer)
	}
}

func TestFillAnswerResponseValidatesWholeObjectiveAnswerGroups(t *testing.T) {
	for _, tt := range []struct {
		name         string
		questionType int
		options      []string
		answers      []string
		want         []string
	}{
		{"partial multiple group", 1, []string{"苹果汁", "香蕉汁"}, []string{"苹果汁", "完全无关"}, nil},
		{"conflicting single group", 0, []string{"苹果汁", "香蕉汁"}, []string{"苹果汁", "香蕉汁"}, nil},
		{"conflicting judgement group", 3, []string{"正确", "错误"}, []string{"正确", "错误"}, nil},
		{"duplicate option text", 0, []string{"same", "same"}, []string{"same"}, nil},
		{"mixed exact and fuzzy multiple group", 1, []string{"abcdefghijklmnop", "abcdefghijklmnoq", "somethingelse"}, []string{"abcdefghijklmnop", "somethingels"}, []string{"abcdefghijklmnop", "somethingelse"}},
		{"judgement synonym", 3, []string{"正确", "错误"}, []string{"对"}, []string{"正确"}},
		{"single member multiple group", 1, []string{"苹果汁", "香蕉汁"}, []string{"苹果汁"}, []string{"苹果汁"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{tt.answers}, &model.SearchRequest{Type: tt.questionType, Options: tt.options})
			if len(resp.Answer.BestAnswer) != len(tt.want) || (len(tt.want) > 0 && !reflect.DeepEqual(resp.Answer.BestAnswer, tt.want)) {
				t.Fatalf("want %v, got %v", tt.want, resp.Answer.BestAnswer)
			}
		})
	}
}
