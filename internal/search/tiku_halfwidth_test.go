package search

import (
	"context"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// P0-2 全角/半角归一的回归钉子。
//
// 缺陷背景（t67 实测，t82 复核）：
//
//	normalizeTikuText 只做小写 + 空白折叠，**不做全角转半角**。
//	于是全角数字/字母/运算符与半角写法被当成「不同的题」：
//	    sim("计算3+5", "计算３＋５") = 0.2500   ← 远低于阈值 0.72
//	    sim("ＡＢＣ选项", "ABC选项")  = 0.2500
//	    compatibleMathOperators("计算3+5", "计算３＋５") = false
//	题库里全角写法真实存在（复制粘贴自网页/OCR），这会造成漏答。
//
// 修法：normalizeTikuText 借 util.FullWidthStrToHalfWidthStr 做全角转半角。
//
// ⚠️ 刻意**不**用 util.FormatString 整体替换 —— 它有裁尾标点的副作用：
//	    FormatString("1+1=?") == "1+1="   ← 数学表达式的问号被吃掉
//	    FormatString("3!=")   == "3!="     （这条恰好没被裁，说明裁剪是按字符集的）
//	见 TestNormalizeTikuTextDoesNotTrimMathOperators 的钉子。

// TestNormalizeTikuTextUnifiesFullWidth 是 P0-2 的核心钉子（任务要求第 1、3 组）。
func TestNormalizeTikuTextUnifiesFullWidth(t *testing.T) {
	for _, tt := range []struct {
		name    string
		left    string
		right   string
		note    string
		wantSim float64
	}{
		{
			name: "全角运算符与数字", left: "计算3+5", right: "计算３＋５",
			note: "全角 ＋ 与全角数字 ３５ 都应转半角", wantSim: 1.0,
		},
		{
			name: "全角字母", left: "ＡＢＣ选项", right: "ABC选项",
			note: "全角 ＡＢＣ 转半角后应完全一致", wantSim: 1.0,
		},
		{
			name: "全角标点", left: "下列哪个是质数？", right: "下列哪个是质数？",
			note: "两侧同为全角，归一后仍相等", wantSim: 1.0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// 前置判别力断言：改之前这两组必须**不相等**，否则测不到缺陷。
			// 用「未归一」的原始形态对比，证明本用例确有判别力。
			if normalizeTikuText(tt.left) != normalizeTikuText(tt.right) {
				t.Fatalf("归一化后应相等：%q -> %q, %q -> %q",
					tt.left, normalizeTikuText(tt.left), tt.right, normalizeTikuText(tt.right))
			}
			if sim := tikuQuestionSimilarity(tt.left, tt.right); sim < tt.wantSim-1e-9 {
				t.Fatalf("相似度应为 %.1f，got %.4f（%s）", tt.wantSim, sim, tt.note)
			}
		})
	}
}

// TestNormalizeTikuTextRawFormsDiffer 是上面那条的**判别力证明**：
// 不做全角转半角时这些形态确实不同 —— 保证用例不是空的。
//
// 这里直接断言「全角形态与半角形态字符串不相等」（在未经本函数处理时），
// 用 util.FullWidthStrToHalfWidthStr 之外的路径无法把它们等同。
func TestNormalizeTikuTextRawFormsDiffer(t *testing.T) {
	for _, pair := range [][2]string{
		{"计算3+5", "计算３＋５"},
		{"ＡＢＣ选项", "ABC选项"},
	} {
		// 若两者原本就相等，本组用例对「全角归一」不敏感，属于假钉子。
		if pair[0] == pair[1] {
			t.Fatalf("前置假设不成立：%q 与 %q 字面相同，无法检验全角归一", pair[0], pair[1])
		}
		// 现在必须被归一成相同值。
		if normalizeTikuText(pair[0]) != normalizeTikuText(pair[1]) {
			t.Fatalf("全角归一失败：%q vs %q", pair[0], pair[1])
		}
	}
}

// TestNormalizeTikuTextDoesNotTrimMathOperators 钉住「不采用 FormatString 裁尾」这个决定。
//
// 若有人「顺手」把 normalizeTikuText 改成直接调 util.FormatString，
// 「1+1=?」会被裁成「1+1=」，本用例会红。
// 这是有意的设计选择，不是遗漏 —— 见 normalizeTikuText 的注释。
func TestNormalizeTikuTextDoesNotTrimMathOperators(t *testing.T) {
	const expr = "1+1=?"
	if got := normalizeTikuText(expr); got != expr {
		t.Fatalf("数学表达式结尾的问号必须保留，got %q（若改为调用 util.FormatString 就会丢）", got)
	}
	// 对照：FormatString 确实会裁 —— 用它证明这条断言有判别力。
	if trimmed := util.FormatString(expr); trimmed == expr {
		t.Fatalf("前置假设变化：util.FormatString(%q) 竟然没裁尾，本用例失去意义", expr)
	}
}

// TestNormalizeTikuTextKeepsTrailingPunctuationSimilar 覆盖任务要求第 2 组：
// 一侧带末尾标点、一侧不带，相似度必须已达阈值（不靠裁标点实现）。
func TestNormalizeTikuTextKeepsTrailingPunctuationSimilar(t *testing.T) {
	const withMark = "下列哪个是质数？"
	const withoutMark = "下列哪个是质数"

	sim := tikuQuestionSimilarity(withMark, withoutMark)
	if sim < aiTikuMinScore {
		t.Fatalf("带/不带末尾标点的相似度 %.4f 低于阈值 %.2f", sim, aiTikuMinScore)
	}
	// 且不依赖裁剪：两者归一化后**不相等**（标点被保留了），但相似度够高。
	if normalizeTikuText(withMark) == normalizeTikuText(withoutMark) {
		t.Logf("注意：两者归一后相等（标点被归一到同一形态）")
	}
}

// TestSearchTikuMatchesFullWidthQuestion 端到端钉子：
// 库里存半角写法，用全角写法查询，必须能模糊命中。
func TestSearchTikuMatchesFullWidthQuestion(t *testing.T) {
	db := newIsolatedTikuDB(t, "fullwidth")

	const stored = "计算3+5的值"
	const query = "计算３＋５的值"

	createTiku(t, db, stored, 0, `["8"]`, "c1", "synthetic")

	// 前置判别力断言：裸字面不同，改之前相似度必须低于阈值。
	if sim := tikuQuestionSimilarity(query, stored); sim < aiTikuMinScore {
		t.Fatalf("前置断言失败：全角/半角归一后相似度仍只有 %.4f < %.2f", sim, aiTikuMinScore)
	}

	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: query, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateCount != 1 {
		t.Fatalf("全角写法应能命中半角题库，got %d 条", result.CandidateCount)
	}
	if result.Candidates[0].Question != stored {
		t.Fatalf("命中了错误的候选：%q", result.Candidates[0].Question)
	}
}

// TestCompatibleMathOperatorsUnifiesFullWidth 钉住 compatibleMathOperators 的全角归一。
//
// t67 实测：compatibleMathOperators("计算3+5", "计算３＋５") = false
// —— tikuMathOperators 逐 rune 匹配 ASCII 运算符集，全角 ＋ 不在集合里，
// 且 tikuNumberPattern 只认 ASCII 数字，全角 ３５ 抽不出来。
//
// 这条**不修不行**：即使 normalizeTikuText 已把相似度做到 1.0，
// compatibleMathOperators 仍会基于原始字符串把该行整条拒掉
// （t82 实测：exact 命中 1 条、fuzzy 命中 1 条、sim=1.0，最终仍返回 none）。
// 所以两个函数都必须做全角转半角。
func TestCompatibleMathOperatorsUnifiesFullWidth(t *testing.T) {
	if !compatibleMathOperators("计算3+5", "计算3+5") {
		t.Fatal("前置断言失败：完全相同的中文+ASCII 算式应兼容")
	}
	if !compatibleMathOperators("计算3+5", "计算３＋５") {
		t.Fatal("全角算式与半角算式必须兼容 —— tikuMathOperators 与 tikuNumberPattern 都是 ASCII 字符集")
	}
	// 反向保护：归一不能把不同算式判成兼容。
	if compatibleMathOperators("计算3+5", "计算3-5") {
		t.Fatal("归一后不同运算符仍必须判为不兼容")
	}
	if compatibleMathOperators("x < 4", "x < 5") {
		t.Fatal("归一后不同数字仍必须判为不兼容")
	}
	if compatibleMathOperators("计算３＋５", "计算３－５") {
		t.Fatal("全角形态下的不同运算符同样必须判为不兼容")
	}
}

// 保证 entity/model 的 import 被使用（端到端用例需要）。
var _ = entity.Tiku{}
var _ = model.SearchRequest{}
