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

// 填空题/连线题（type=2）与简答题/阅读理解（type=4/-4）同属非客观题，
// 答案正文必须原样返回：这些题的答案是用户要填进空里的原文，
// 改写标点或全角字符会让填进去的答案与被考答案不一致。
//
// 参考依据（chaoxingexam/subjective_answers.py）：
//   - :13  SUBJECTIVE_TYPES 明确包含「填空题」
//   - :136-149 clean_html_tags 只清 HTML 标签、保留标点，不做标点归一化
func TestFillAnswerResponsePreservesBlankQuestionAnswerTextVerbatim(t *testing.T) {
	rawAnswers := []string{
		"这是答案。",   // 句号
		"因为：所以",   // 全角冒号
		"为什么？",    // 问号
		"他说：“好的”", // 中文引号
		"ＡＢＣ",     // 全角字母
		"1.5",     // 小数点（必须不被去尾)
		"《红楼梦》",   // 书名号
	}
	for _, raw := range rawAnswers {
		t.Run(raw, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{raw}}, &model.SearchRequest{
				Question: "请填空",
				Type:     2,
			})
			if !reflect.DeepEqual(resp.Answer.BestAnswer, []string{raw}) {
				t.Fatalf("type=2 答案必须原样保留：want %q, got %q", raw, resp.Answer.BestAnswer)
			}
			if resp.Answer.AnswerText != raw {
				t.Fatalf("type=2 answerText 必须原样保留：want %q, got %q", raw, resp.Answer.AnswerText)
			}
		})
	}
}

// 同一份答案文本在 type=2 与 type=4 下必须得到相同结果（对称性钉子）：
// 两者都是非客观题，不该因为题型编号不同而改写正文。
func TestFillAnswerResponseTreatsBlankAndEssayTypesSymmetrically(t *testing.T) {
	const raw = "因为：所以。"
	type2 := FillAnswerResponse([][]string{{raw}}, &model.SearchRequest{Question: "q", Type: 2})
	type4 := FillAnswerResponse([][]string{{raw}}, &model.SearchRequest{Question: "q", Type: 4})
	typeNeg4 := FillAnswerResponse([][]string{{raw}}, &model.SearchRequest{Question: "q", Type: -4})
	if type2.Answer.AnswerText != type4.Answer.AnswerText || type4.Answer.AnswerText != typeNeg4.Answer.AnswerText {
		t.Fatalf("非客观题答案正文处理必须一致：type=2 %q, type=4 %q, type=-4 %q",
			type2.Answer.AnswerText, type4.Answer.AnswerText, typeNeg4.Answer.AnswerText)
	}
}

// 判断题的选项常常就是 A/B 两值（题库普遍用 A=正确、B=错误），
// 因此纯字母答案必须在**判断题**上映射到对应选项。
//
// 依据（chaoxing-exam 参考项目）：
//   - references/answer-json-format.md:14「判断题的 answer 值是 true/false，
//     不是 A/B」→ 字母与选项值是两套表示，服务端必须能解析字母；
//   - SKILL.md:272-278 陷阱 4「填写答案时必须用显示字母匹配」。
//
// 范围刻意收窄：只在 type=3 且选项**没有**对错语义（如 A/B）时按标号解析。
// 单选/多选保持既有语义 —— 那里「B」既可能是标号也可能是某个选项正文，
// 既有测试 TestFillAnswerResponseStripsOptionLabelRepeatedByProvider 的
// "letter-only answer stays unmapped" 子用例有意钉住了该行为（commit 0b171b6）。
func TestFillAnswerResponseResolvesOptionLabelForJudgementQuestions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		answer   string
		options  []string
		wantBest []string
		wantKey  string
	}{
		{"A映射到正确", "A", []string{"正确", "错误"}, []string{"正确"}, "A"},
		{"B映射到错误", "B", []string{"正确", "错误"}, []string{"错误"}, "B"},
		{"小写a", "a", []string{"正确", "错误"}, []string{"正确"}, "A"},
		{"带点号", "B.", []string{"正确", "错误"}, []string{"错误"}, "B"},
		{"括号", "(A)", []string{"正确", "错误"}, []string{"正确"}, "A"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{
				Question: "判断",
				Options:  append([]string(nil), tt.options...),
				Type:     3,
			})
			if !reflect.DeepEqual(resp.Answer.BestAnswer, tt.wantBest) {
				t.Fatalf("best answer: want %v, got %v", tt.wantBest, resp.Answer.BestAnswer)
			}
			if resp.Answer.AnswerKeyText != tt.wantKey {
				t.Fatalf("answer key: want %q, got %q", tt.wantKey, resp.Answer.AnswerKeyText)
			}
		})
	}
}

// 判断题用 对/错、√/× 等语义值作答时，既有极性映射必须仍然优先，
// 不能被新增的标号解析抢走。
func TestFillAnswerResponseKeepsPolarityMappingBeforeLabelResolution(t *testing.T) {
	for _, tt := range []struct {
		answer   string
		wantBest string
		wantKey  string
	}{
		{"对", "正确", "A"},
		{"错", "错误", "B"},
		{"√", "正确", "A"},
		{"×", "错误", "B"},
		{"true", "正确", "A"},
		{"false", "错误", "B"},
	} {
		t.Run(tt.answer, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{
				Question: "判断",
				Options:  []string{"正确", "错误"},
				Type:     3,
			})
			if !reflect.DeepEqual(resp.Answer.BestAnswer, []string{tt.wantBest}) {
				t.Fatalf("want %v, got %v", tt.wantBest, resp.Answer.BestAnswer)
			}
			if resp.Answer.AnswerKeyText != tt.wantKey {
				t.Fatalf("want key %q, got %q", tt.wantKey, resp.Answer.AnswerKeyText)
			}
		})
	}
}

// 判断题选项本身带对错语义时，不该再走标号解析：
// ["正确","错误"] 上答 "A" 仍按标号（选项无极性），
// 但 ["对","错"] 上答 "A" 也一样 —— 关键是越界标号必须被拒绝。
func TestFillAnswerResponseRejectsOutOfRangeOptionLabelForJudgement(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"C"}}, &model.SearchRequest{
		Question: "判断",
		Options:  []string{"正确", "错误"},
		Type:     3,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("越界标号必须被拒绝，got %v", resp.Answer.BestAnswer)
	}
}

// 单选/多选的纯字母答案维持既有语义（不映射），这是刻意保留的边界：
// 该行为由 TestFillAnswerResponseStripsOptionLabelRepeatedByProvider 的
// "letter-only answer stays unmapped" 子用例锁定，此处再显式钉一次，
// 防止将来有人顺手把标号解析扩到全部客观题。
func TestFillAnswerResponseDoesNotResolveOptionLabelForChoiceQuestions(t *testing.T) {
	options := []string{"甲", "乙", "丙"}
	for _, qType := range []int{0, 1} {
		resp := FillAnswerResponse([][]string{{"B"}}, &model.SearchRequest{
			Question: "选择",
			Options:  append([]string(nil), options...),
			Type:     qType,
		})
		if len(resp.Answer.BestAnswer) != 0 {
			t.Fatalf("type=%d 的纯字母答案维持既有语义（不映射），got %v", qType, resp.Answer.BestAnswer)
		}
	}
}

// captain 提出的「条件 3：选项内容不是单个字母」在 type=3 上**结构性成立**，
// 无需额外分支 —— 因为 FormatOptions(type=3) 会用 IsTrue/IsFalse 把 A/B 改写掉：
//
//	IsTrue("A")=true  -> "正确"
//	IsFalse("B")=true -> "错误"
//
// 而答案侧 JudgementPolarity **不认** A/B（只有选项侧认）。
// 这条不对称正是「答案 A 在判断题上拿不到答案」的根因：
// 选项被改写成了 正确/错误，答案却停在字母 A，两侧对不上。
//
// 本用例把这个不对称钉住，防止有人「顺手」把选项侧的 A/B 支持删掉
// （那会让 [正确,错误] 与 [A,B] 两种写法的题库表现不一致）。
func TestFormatOptionsNormalizesLetterOptionsForJudgementButPolarityDoesNot(t *testing.T) {
	// 选项侧：A/B 被归一化成 正确/错误
	if got := FormatOptions([]string{"A", "B"}, 3); !reflect.DeepEqual(got, []string{"正确", "错误"}) {
		t.Fatalf("判断题选项侧应把 A/B 归一化为 正确/错误，got %v", got)
	}
	// 答案侧：A/B 不被极性识别（所以必须靠标号解析兜底）
	for _, letter := range []string{"A", "B", "a", "b"} {
		if _, ok := JudgementPolarity(letter); ok {
			t.Fatalf("JudgementPolarity 不应识别字母 %q —— 若改为识别，需同步删除 optionLabelIndex 并重测全部答案形态", letter)
		}
	}
}

// 选项字面就是 A/B 的判断题：归一化后仍是 正确/错误，
// 答案 "B" 映射到「错误」是自洽的（选项语义上就是 [正确,错误]）。
func TestFillAnswerResponseHandlesLetterOptionsForJudgement(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"B"}}, &model.SearchRequest{
		Question: "判断",
		Options:  []string{"A", "B"},
		Type:     3,
	})
	if !reflect.DeepEqual(resp.Answer.BestAnswer, []string{"错误"}) {
		t.Fatalf("want [错误], got %v", resp.Answer.BestAnswer)
	}
	if resp.Answer.AnswerKeyText != "B" {
		t.Fatalf("want key B, got %q", resp.Answer.AnswerKeyText)
	}
}
