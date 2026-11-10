package util

import (
	"reflect"
	"testing"
)

// 本文件为 answer_aggregate.go 的加权聚合提供回归钉子。
//
// 依据（上游 chaoxing-exam/README.md:28、:35）：
//
//	「单一模型直接看图答题，实测 50 题错 3 题。拆成『看』和『想』两步，
//	 各取所长，准确率显著提升。」
//	「首次答题正确率约 65-75%（取决于题目难度）。但超星系统提交后会显示标准答案」
//
// 上游的兜底是「提交后拿标准答案重做」；本项目没有那条回路，
// 因此「证据不足时弃权」必须是**显式选项**，不能悄悄改变默认行为 ——
// 否则题库只有一个来源的题会从「有答案」退化成「无答案」。

// 零值 options 必须与 SearchRightAnswer 逐字节等价。
//
// 这是本层最重要的不变量：SearchRightAnswerWeighted 会被当作
// SearchRightAnswer 的「可直接替换」版本接入调用方，
// 一旦零值行为漂移，替换就会静默改变线上答案。
func TestAggregateAnswersZeroOptionsMatchesSearchRightAnswer(t *testing.T) {
	cases := [][][]string{
		{{"甲"}},
		{{"甲"}, {"乙"}},
		{{"乙"}, {"甲"}, {"甲"}}, // 有明确多数，且多数组不在首位
		{{"甲", "丙"}, {"甲", "丙"}, {"乙"}}, // 多元素组
		{{"甲"}, {"乙"}, {"丙"}},           // 三方并列
		{{}},
		{{""}},
		{},
		nil,
	}
	for i, answers := range cases {
		want := SearchRightAnswer(answers)
		got := SearchRightAnswerWeighted(answers, AnswerAggregationOptions{})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: 零值 options 必须与 SearchRightAnswer 一致，want %v, got %v", i, want, got)
		}
	}
}

// AbstainOnTie=true 时，并列必须弃权 —— 而不是硬猜第一个。
//
// 这条是「加权聚合」与「计票取第一」的语义分界线：
// 若注入坏实现取 winners[0]，本用例必须变红。
func TestAggregateAnswersAbstainsOnTie(t *testing.T) {
	answers := [][]string{{"甲"}, {"乙"}}
	got := AggregateAnswers(answers, AnswerAggregationOptions{AbstainOnTie: true})
	if len(got.Answer) != 0 {
		t.Fatalf("并列时启用 AbstainOnTie 必须弃权，got %v", got.Answer)
	}
	if !got.LowConfidence {
		t.Fatalf("并列弃权必须标记 LowConfidence")
	}
	if got.Total != 2 {
		t.Fatalf("Total 必须是参与聚合的组数 2，got %d", got.Total)
	}
	if got.Confidence != 0.5 {
		t.Fatalf("两组并列的置信度必须是 0.5，got %v", got.Confidence)
	}
	// 反向钉子：默认（不弃权）必须**有答案**，否则「弃权」开关就没有区分度。
	fallback := AggregateAnswers(answers, AnswerAggregationOptions{})
	if !reflect.DeepEqual(fallback.Answer, []string{"甲"}) {
		t.Fatalf("默认行为必须保持「取最先出现」，got %v", fallback.Answer)
	}
}

// 非并列时，AbstainOnTie 不应把有明确多数的答案也弃掉。
func TestAggregateAnswersKeepsClearMajorityEvenWhenAbstainEnabled(t *testing.T) {
	answers := [][]string{{"乙"}, {"甲"}, {"甲"}}
	got := AggregateAnswers(answers, AnswerAggregationOptions{AbstainOnTie: true})
	if !reflect.DeepEqual(got.Answer, []string{"甲"}) {
		t.Fatalf("明确多数必须胜出，got %v", got.Answer)
	}
	if got.LowConfidence {
		t.Fatalf("明确多数不该被标记 LowConfidence")
	}
	if got.Support != 2 {
		t.Fatalf("Support 必须是 2，got %d", got.Support)
	}
	// 2/3 的权重占比。
	if diff := got.Confidence - 2.0/3.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("Confidence 必须是 2/3，got %v", got.Confidence)
	}
}

// 单来源输入永远是高置信度的 —— 「只有一条证据」谈不上冲突。
// 这条防止把「来源少」误报成「低置信」而触发上层不必要的人工介入。
func TestAggregateAnswersSingleSourceIsHighConfidence(t *testing.T) {
	got := AggregateAnswers([][]string{{"甲"}}, AnswerAggregationOptions{AbstainOnTie: true})
	if !reflect.DeepEqual(got.Answer, []string{"甲"}) {
		t.Fatalf("单来源必须给出答案，got %v", got.Answer)
	}
	if got.LowConfidence {
		t.Fatalf("单来源不该被标记 LowConfidence")
	}
	if got.Confidence != 1.0 {
		t.Fatalf("单来源置信度必须是 1.0，got %v", got.Confidence)
	}
}

// PositionDecay 必须真的按位置衰减权重：靠后的组权重小，能被靠前的组压过。
//
// 若注入坏实现把 weight 恒置 1，本用例会变红（结果退回「并列弃权/取第一」）。
func TestAggregateAnswersPositionDecayBreaksTieTowardEarlierGroups(t *testing.T) {
	answers := [][]string{{"甲"}, {"乙"}}
	got := AggregateAnswers(answers, AnswerAggregationOptions{
		PositionDecay: 2, // 甲=1, 乙=2 → 乙胜出
		AbstainOnTie:  true,
	})
	if !reflect.DeepEqual(got.Answer, []string{"乙"}) {
		t.Fatalf("PositionDecay 应让后出现但权重更高的组胜出，got %v", got.Answer)
	}
	if got.LowConfidence {
		t.Fatalf("衰减后不再是并列，不该标记 LowConfidence")
	}
	// 反向钉子：不衰减时这两组是并列。
	flat := AggregateAnswers(answers, AnswerAggregationOptions{AbstainOnTie: true})
	if len(flat.Answer) != 0 {
		t.Fatalf("不衰减时两组必须并列并弃权，got %v", flat.Answer)
	}
}

// MinSupport 门槛：只有 1 个来源给出的答案，在要求 ≥2 支持时应当被拒。
func TestAggregateAnswersHonoursMinSupport(t *testing.T) {
	answers := [][]string{{"甲"}, {"乙"}, {"乙"}}
	got := AggregateAnswers(answers, AnswerAggregationOptions{MinSupport: 2})
	if !reflect.DeepEqual(got.Answer, []string{"乙"}) {
		t.Fatalf("乙有 2 个来源支持，应胜出，got %v", got.Answer)
	}

	weak := AggregateAnswers([][]string{{"甲"}, {"乙"}, {"丙"}}, AnswerAggregationOptions{MinSupport: 2})
	if len(weak.Answer) != 0 {
		t.Fatalf("每组都只有 1 个来源，MinSupport=2 必须拒答，got %v", weak.Answer)
	}
}

// 返回的答案必须是副本：调用方改写它不能污染入参。
func TestAggregateAnswersReturnsCopy(t *testing.T) {
	answers := [][]string{{"甲", "乙"}}
	got := AggregateAnswers(answers, AnswerAggregationOptions{})
	got.Answer[0] = "被改写"
	if answers[0][0] != "甲" {
		t.Fatalf("返回值必须是副本，入参已被污染：%v", answers)
	}
}
