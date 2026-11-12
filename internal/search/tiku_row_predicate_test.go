package search

import (
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// 「行可用性判定唯一化」的回归钉子（t126 重构）。
//
// 重构前的问题：
//
//	主过滤循环（SearchTiku）与 hasUsableTikuRow 各写了一遍同样的三个条件。
//	P0-1 给主循环加 compatibleNegation 时，hasUsableTikuRow 就漏了 ——
//	「判定可用性」与「实际采用」两处漂移，而那正是 hasUsableTikuRow
//	存在意义所要防的事，结果它自己成了漂移源。
//
// 重构后：两者共用 compatibleTikuRow 一个谓词。本文件钉住这个不变量。

// TestCompatibleTikuRowIsSingleSourceOfTruth 直接验证谓词的各维度。
func TestCompatibleTikuRowIsSingleSourceOfTruth(t *testing.T) {
	baseRow := tikuCandidateRow{
		Question: "下列属于生物的是", Type: 0, Options: "[]", Answer: `["细胞"]`,
	}
	baseReq := model.SearchRequest{Question: "下列属于生物的是", Type: 0}

	if !compatibleTikuRow(baseReq, baseReq.Question, baseRow) {
		t.Fatal("前置断言失败：完全匹配的行必须判为可用")
	}

	for _, tt := range []struct {
		name string
		req  model.SearchRequest
		row  tikuCandidateRow
	}{
		{
			name: "题型不兼容",
			req:  model.SearchRequest{Question: baseReq.Question, Type: 1},
			row:  baseRow,
		},
		{
			name: "数学模型不符",
			req:  model.SearchRequest{Question: "计算 3+5", Type: 0},
			row:  tikuCandidateRow{Question: "计算 3-5", Type: 0, Options: "[]", Answer: `["8"]`},
		},
		{
			name: "否极相反",
			req:  model.SearchRequest{Question: "下列属于生物的是", Type: 0},
			row:  tikuCandidateRow{Question: "下列不属于生物的是", Type: 0, Options: "[]", Answer: `["细胞"]`},
		},
		{
			name: "选项集合冲突",
			req:  model.SearchRequest{Question: baseReq.Question, Type: 0, Options: []string{"甲", "乙"}},
			row:  tikuCandidateRow{Question: baseReq.Question, Type: 0, Options: `["丙","丁"]`, Answer: `["细胞"]`},
		},
		{
			name: "答案为空数组",
			req:  model.SearchRequest{Question: baseReq.Question, Type: 0},
			row:  tikuCandidateRow{Question: baseReq.Question, Type: 0, Options: "[]", Answer: `[]`},
		},
		{
			name: "答案不是合法 JSON",
			req:  model.SearchRequest{Question: baseReq.Question, Type: 0},
			row:  tikuCandidateRow{Question: baseReq.Question, Type: 0, Options: "[]", Answer: `not-json`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if compatibleTikuRow(tt.req, tt.req.Question, tt.row) {
				t.Fatalf("应判为不可用，实际判可用：row=%+v", tt.row)
			}
		})
	}
}

// TestHasUsableTikuRowDelegatesToCompatibleTikuRow 钉住量词层的等价性。
//
// 关键不变量：对**任意**单行输入，
//
//	hasUsableTikuRow([row]) == compatibleTikuRow(row)
//
// 若将来有人在 hasUsableTikuRow 里加了自己的额外条件（漂移的典型形态），
// 这条等价性会破，测试变红。
func TestHasUsableTikuRowDelegatesToCompatibleTikuRow(t *testing.T) {
	req := model.SearchRequest{Question: "下列属于生物的是", Type: 0}

	rows := []tikuCandidateRow{
		// 可用
		{Question: "下列属于生物的是", Type: 0, Options: "[]", Answer: `["细胞"]`},
		// 各维度的不可用
		{Question: "下列属于生物的是", Type: 1, Options: "[]", Answer: `["细胞"]`},
		{Question: "下列不属于生物的是", Type: 0, Options: "[]", Answer: `["细胞"]`},
		{Question: "下列属于生物的是", Type: 0, Options: "[]", Answer: `[]`},
		{Question: "计算 3-5", Type: 0, Options: "[]", Answer: `["8"]`},
	}

	for i, row := range rows {
		single := hasUsableTikuRow([]tikuCandidateRow{row}, req)
		direct := compatibleTikuRow(req, req.Question, row)
		if single != direct {
			t.Fatalf("第 %d 行量词层与谓词层不一致：hasUsableTikuRow=%v compatibleTikuRow=%v（说明两处又漂移了）row=%+v",
				i, single, direct, row)
		}
	}
}

// TestHasUsableTikuRowAnySemantics 钉住「任一行可用即 true」的语义。
func TestHasUsableTikuRowAnySemantics(t *testing.T) {
	req := model.SearchRequest{Question: "下列属于生物的是", Type: 0}
	good := tikuCandidateRow{Question: "下列属于生物的是", Type: 0, Options: "[]", Answer: `["细胞"]`}
	bad := []tikuCandidateRow{
		{Question: "下列属于生物的是", Type: 1, Options: "[]", Answer: `["细胞"]`},
		{Question: "下列属于生物的是", Type: 0, Options: "[]", Answer: `[]`},
	}

	if hasUsableTikuRow(nil, req) {
		t.Fatal("空集合必须返回 false")
	}
	if hasUsableTikuRow(bad, req) {
		t.Fatal("全不可用必须返回 false")
	}
	if !hasUsableTikuRow(append(append([]tikuCandidateRow{}, bad...), good), req) {
		t.Fatal("只要有一条可用就必须返回 true（任意位置）")
	}
	// 可用行放在最前面也必须被识别（不能只扫固定位置）
	if !hasUsableTikuRow(append([]tikuCandidateRow{good}, bad...), req) {
		t.Fatal("可用行在首位时必须返回 true")
	}
}

// TestCompatibleTikuRowExcludesFuzzyThreshold 钉住「阈值不进谓词」这个设计决策。
//
// 为什么必须钉：若把 aiTikuMinScore 塞进 compatibleTikuRow，
// hasUsableTikuRow 会把「exact 池里有一条低相似度行」误判成「池不可用」，
// 于是在根本不需要 fuzzy 的场景下多做一次全表 INSTR 查询（性能回归）。
//
// 这条用例构造「题干相似度极低但行本身可用」的情形：
// 谓词必须判可用（阈值判定留给 fuzzy 分支）。
func TestCompatibleTikuRowExcludesFuzzyThreshold(t *testing.T) {
	req := model.SearchRequest{Question: "下列属于生物的是", Type: 0}
	// 题干与查询几乎无关，但类型/选项/答案都合法 → 行本身可用。
	row := tikuCandidateRow{
		Question: "完全不相关的另一道题关于天体物理", Type: 0, Options: "[]", Answer: `["某答案"]`,
	}

	// 前置确认：这两句的相似度确实低于阈值（否则本用例没构造到目标情形）。
	if sim := tikuQuestionSimilarity(req.Question, row.Question); sim >= aiTikuMinScore {
		t.Fatalf("前置断言失败：相似度 %.4f 未低于阈值 %.2f，本用例没构造到目标情形",
			sim, aiTikuMinScore)
	}

	if !compatibleTikuRow(req, req.Question, row) {
		t.Fatal("谓词不应包含相似度阈值判定 —— 阈值只属于 fuzzy 分支（否则 exact 池会被误判为不可用）")
	}
	if !hasUsableTikuRow([]tikuCandidateRow{row}, req) {
		t.Fatal("同一行在量词层也必须判可用")
	}
}
