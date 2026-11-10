package search

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// 否定问法保护（P0-1）的回归钉子。
//
// 缺陷背景（t67 审查实测）：
//
//	sim("...叙述正确的是", "...不正确的是") = 0.8966
//	sim("以下说法正确",   "以下说法不正确")  = 0.7273
//	sim("可以发生反应",   "不可以发生反应")  = 0.9091
//
// 全部 >= aiTikuMinScore=0.72 —— bigram 把「不」稀释进二十来个 bigram 里
// 几乎无影响。这是唯一会**主动返回错误答案**的匹配缺陷。
//
// 修法见 tiku_search.go 的 compatibleNegation：抽「是否否定问法」布尔特征，
// 两侧不等则直接拒绝（不降分、不动 0.72 阈值）。

// newNegationFixtureDB 建库并插入若干题干，返回可直接检索的 tool。
func newNegationFixtureDB(t *testing.T, questions ...string) *DBTikuSearchTool {
	t.Helper()
	db := newTikuSearchTestDB(t)
	for index, question := range questions {
		encodedAnswer, err := json.Marshal([]string{question + "-answer-" + string(rune('a'+index))})
		if err != nil {
			t.Fatal(err)
		}
		hash := util.BuildTikuHashCandidates(question, nil, 0)[0]
		if err := db.Create(&entity.Tiku{
			Question: question, Type: 0, Options: "[]", Answer: string(encodedAnswer),
			Hash: hash, SourceName: "synthetic",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return NewDBTikuSearchTool(db)
}

// TestTikuNegationGuardRejectsOppositePolarity 覆盖任务要求的第 1/2/3/6 组：
// 一侧有否定、另一侧没有 —— 必须**不匹配**（候选数为 0）。
func TestTikuNegationGuardRejectsOppositePolarity(t *testing.T) {
	for _, tt := range []struct {
		name     string
		query    string
		stored   string
		wantKind string
	}{
		{"叙述是否正确", "下列关于细胞呼吸的叙述正确的是", "下列关于细胞呼吸的叙述不正确的是", "none"},
		{"以下说法正确", "以下说法正确", "以下说法不正确", "none"},
		{"可以发生反应", "可以发生反应", "不可以发生反应", "none"},
		{"属于生物", "下列属于生物的是", "下列不属于生物的是", "none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := newNegationFixtureDB(t, tt.stored)
			result, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: tt.query, Type: 0})
			if err != nil {
				t.Fatal(err)
			}
			// 对照组：先确认这两条题干在**不带**否定判定时是会被模糊命中的，
			// 否则本用例对缺陷不敏感（假钉子）。
			if sim := tikuQuestionSimilarity(tt.query, tt.stored); sim < aiTikuMinScore {
				t.Fatalf("对照假设失败：裸相似度 %.4f 本就低于阈值 %.2f，本用例无判别力（%q / %q）",
					sim, aiTikuMinScore, tt.query, tt.stored)
			}
			if result.CandidateCount != 0 {
				t.Fatalf("否定问法必须拒绝匹配，got kind=%q candidates=%+v",
					result.MatchKind, result.Candidates)
			}
		})
	}
}

// TestTikuNegationGuardKeepsSamePolarity 覆盖第 4/5 组对照组：
// 两侧都否定、或「不」只是构词成分 —— 必须**仍然匹配**。
//
// 这两条是防「见到不字就拒」的粗暴实现的关键：没有它们，
// 只查单侧是否含否定词的实现也能通过上面那个用例。
func TestTikuNegationGuardKeepsSamePolarity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		query  string
		stored string
	}{
		// 第 4 组：两侧都否定 —— 同一道题，必须匹配。
		{"两侧都否定", "下列说法不正确的是", "下列说法不正确的是"},
		// 补充（captain 2026-11-11 要求）：两侧都否定，但**尾部词不同**
		// （`的是` vs `的有`）—— 确认判定不是靠精确匹配整句，而是靠
		// 「是否否定问法」这个语义特征。若实现退化成整句相等比较，本条会红。
		{"两侧都否定但尾部词不同", "以下不属于要求的是", "以下不属于要求的有"},
		// 第 5 组：「不等式」不是否定问法 —— 必须匹配。
		{"不等式是构词", "不等式求解", "不等式求解"},
		// 补充：「不确定性」同理。
		{"不确定性是构词", "不确定性与风险度量", "不确定性与风险度量"},
		// 补充：两侧都无否定 —— 常规题目必须匹配。
		{"常规题面", "下列关于细胞呼吸的叙述正确的是", "下列关于细胞呼吸的叙述正确的是"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := newNegationFixtureDB(t, tt.stored)
			result, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: tt.query, Type: 0})
			if err != nil {
				t.Fatal(err)
			}
			if result.CandidateCount != 1 {
				t.Fatalf("两侧极性一致时必须匹配，got kind=%q candidates=%+v",
					result.MatchKind, result.Candidates)
			}
			if result.Candidates[0].Question != tt.stored {
				t.Fatalf("命中了错误的候选：%q", result.Candidates[0].Question)
			}
		})
	}
}

// TestTikuQuestionNegated 直接钉住否定判定本身，便于定位变异。
func TestTikuQuestionNegated(t *testing.T) {
	for _, tt := range []struct {
		question string
		want     bool
	}{
		// —— 是否定问法 ——
		{"下列关于细胞呼吸的叙述不正确的是", true},
		{"以下说法不正确", true},
		{"不可以发生反应", true},
		{"下列不属于生物的是", true},
		{"以下选项中错误的是", true},
		{"下列说法不对", true},
		{"无法确定的是", true},
		{"没有错误的是", true},
		{"并非所有金属都能导电", true},

		// —— 不是否定问法（「不」作构词成分，不在句尾窗口的否定结构里）——
		{"不等式求解", false},
		{"不确定性与风险度量", false},
		{"下列选项中不同的一项", false},
		{"下列关于细胞呼吸的叙述正确的是", false},
		{"以下说法正确", false},
		{"可以发生反应", false},
		{"下列属于生物的是", false},

		// —— 空串与极短题干（边界保护：tail 取 12 字时 len<12 不得越界/panic）——
		{"", false},
		{"   ", false},
		{"？", false},
		// 单字「不」**刻意不认**：白名单是多字词（最短 2 字），
		// captain 明确要求「抽否定词，2-4 字的多字词优先，不是单字」。
		// 认单字会让「不等式」「不确定性」这类构词立刻全线误伤。
		{"不", false},
		{"是", false},
		{"不正确", true},
		{"属于生物的是", false},
	} {
		t.Run(tt.question, func(t *testing.T) {
			if got := tikuQuestionNegated(tt.question); got != tt.want {
				t.Fatalf("tikuQuestionNegated(%q) = %v, want %v", tt.question, got, tt.want)
			}
		})
	}
}

// TestTikuNegationGuardShortQuestionDoesNotPanic 显式钉住极短题干的边界保护。
//
// tikuQuestionNegated 会取尾部 12 个 rune；输入短于 12 时必须有
// `if start < 0 { start = 0 }` 的保护，否则 `runes[start:]` 会 panic
// （负数下标）。空串走 TrimSpace 早退，但单字符、2-3 字输入会直达切片。
//
// 这条单列出来是因为它测的是**崩溃**而不是语义：语义用例里一条 panic
// 会把整组测试打成 FAIL 但看不出根因。
func TestTikuNegationGuardShortQuestionDoesNotPanic(t *testing.T) {
	for _, question := range []string{"", " ", "？", "不", "不是", "不属于", "不属于的", "下列说法不正确"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("tikuQuestionNegated(%q) panic: %v（tail 切片缺少下界保护）", question, r)
				}
			}()
			_ = tikuQuestionNegated(question)
			_ = compatibleNegation(question, "不属于生物的是")
			_ = compatibleNegation("不属于生物的是", question)
		}()
	}
}

// TestTikuNegationGuardSubstringIsNotBidirectional 钉住「属于」不会被当成「不属于」。
//
// captain 2026-11-11 提出的风险点：`属于` 是 `不属于` 的子串，
// 若实现用的是「白名单词是题干的子串」之外的反向判断（如题干是白名单词的子串），
// 或者白名单里误收了 `属于`，就会出现「属于生物的是」也被判否定 →
// 常规题被拒。本用例从正面（属于=false）与对照（不属于=true）两侧钉住。
func TestTikuNegationGuardSubstringIsNotBidirectional(t *testing.T) {
	const positive = "下列属于生物的是"
	const negative = "下列不属于生物的是"

	if tikuQuestionNegated(positive) {
		t.Fatalf("%q 含「属于」（是「不属于」的子串）但不含否定词，不得判为否定问法", positive)
	}
	if !tikuQuestionNegated(negative) {
		t.Fatalf("%q 含「不属于」，必须判为否定问法", negative)
	}
	if compatibleNegation(positive, negative) {
		t.Fatalf("「属于」与「不属于」必须判为不兼容（否则会把正向题匹配到否定题上）")
	}
}

// TestCompatibleNegationSymmetry 保证判定对称：交换两侧结果不变。
//
// 若实现写成「右侧是 left 的子串」这类方向性判断，对称性会被破坏，
// 而检索方向在 exact/fuzzy 两条路径上是可能互换的。
func TestCompatibleNegationSymmetry(t *testing.T) {
	pairs := [][2]string{
		{"下列说法不正确的是", "下列说法正确的是"},
		{"下列说法不正确的是", "下列说法不正确的是"},
		{"不等式求解", "不等式求解"},
		{"以下说法正确", "以下说法不正确"},
	}
	for _, pair := range pairs {
		forward := compatibleNegation(pair[0], pair[1])
		backward := compatibleNegation(pair[1], pair[0])
		if forward != backward {
			t.Fatalf("compatibleNegation 必须对称：%q/%q forward=%v backward=%v",
				pair[0], pair[1], forward, backward)
		}
	}
}

// TestTikuNegationGuardKeepsThresholdUnchanged 钉住「否定判定是独立判据，不是调阈值」。
//
// 若有人把 aiTikuMinScore 调高来「顺便」挡否定题，本用例会变红 ——
// 那会连带砍掉所有正常题目的召回。
func TestTikuNegationGuardKeepsThresholdUnchanged(t *testing.T) {
	if aiTikuMinScore != 0.72 {
		t.Fatalf("aiTikuMinScore 必须保持 0.72（否定判定是独立拒绝判据，不得靠调阈值实现），got %v", aiTikuMinScore)
	}
}
