package search

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 这一组用例锁的是「Ambiguous 是否进了 CallUsage」这条可观测性缺口。
//
// 缺陷形态：AITikuSearchResult 里有 ambiguous（模型看得到），
// AITikuSearchCallUsage 里没有（运维看不到）⇒ 无法统计模糊命中率，
// 也就无法用数据决定 aiTikuMinScore(0.72) / aiTikuAmbiguityGap(0.08) 该不该调。
//
// 三条用例分别覆盖：模糊且有歧义 / 模糊但无歧义 / exact（不参与歧义判定）。
// 第三条不是凑数：tiku_search.go 的歧义判定挂在 `kind == "fuzzy"` 条件下，
// exact 路径根本不会置位，必须确认 usage 里同样是 false 而不是「碰巧」。

// searchUsageViaTool 走**真实**的 executeAITikuSearchTool 路径，
// 返回它追加进 usage.TikuSearchCalls 的那条记录。
//
// 刻意不用手搓 AITikuSearchCallUsage 再断言：那样只证明结构体能存值，
// 不证明 executeAITikuSearchTool 真的把值传过去了 —— 那才是缺陷所在。
func searchUsageViaTool(t *testing.T, tool AITikuSearchTool, question string, qType int, options []string) (AITikuSearchCallUsage, AITikuSearchResult) {
	t.Helper()
	client := &ChatGptClient{TikuSearchTool: tool}
	web, local := 0, 0
	usage := AISearchUsage{}
	arguments, _ := json.Marshal(map[string]any{"question": question, "type": qType, "options": options})
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "tiku_search", Arguments: string(arguments)}}
	if _, abort := client.executeAITikuSearchTool(context.Background(), call, &web, &local, 4, map[string]string{}, nil, &usage); abort {
		t.Fatalf("本地题库检索不应触发 abort：%+v", usage)
	}
	if len(usage.TikuSearchCalls) != 1 {
		t.Fatalf("期望恰好 1 条 usage 记录，实际 %d 条：%+v", len(usage.TikuSearchCalls), usage.TikuSearchCalls)
	}
	// 顺带取回原始结果，便于在失败信息里同时对比「结果侧」与「usage 侧」的值。
	result, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: question, Type: qType, Options: options})
	if err != nil {
		t.Fatalf("SearchTiku 复算失败：%v", err)
	}
	return usage.TikuSearchCalls[0], result
}

// 用例 1：模糊匹配且前两名分差 < aiTikuAmbiguityGap ⇒ usage.ambiguous 必须为 true。
//
// 构造方式：让题库里存在两条与题干高度相似但**答案不同**的记录，
// 这样既能命中「前两名分差 < gap」这条，也命中「答案集合不唯一」那条 ——
// 两条路径都会置 result.Ambiguous，本用例只要求 usage 能透传出来。
// 顺序依赖：这也是「候选 ≥2 且都过 aiTikuMinScore」的最小构造。
func TestAITikuSearchCallUsageCarriesAmbiguousOnFuzzyTie(t *testing.T) {
	db := newTikuSearchTestDB(t)
	// 题干与两条记录都只差一个无关紧要的尾字，相似度必然远高于 0.72；
	// 两条记录彼此答案不同 ⇒ answerSets>1 且分差通常 < 0.08。
	//
	// ⚠️ 题干必须与所有候选行**哈希不同**，否则会走 exact 而不是 fuzzy：
	// BuildTikuHashCandidates 会把问号/空白归一化，`...是什么` 与 `...是什么？`
	// 会命中同一个 hash。因此这里让题干与候选题干的措辞有意错开（改动词而非只改标点）。
	question := "说明植物进行光合作用的主要场所"
	rows := []struct {
		question string
		answer   []string
	}{
		{"植物进行光合作用的主要场所是什么？", []string{"叶绿体"}},
		{"植物进行光合作用的主要场所在哪里？", []string{"线粒体"}},
	}
	for _, row := range rows {
		encodedOptions, _ := json.Marshal([]string{})
		encodedAnswer, _ := json.Marshal(row.answer)
		hash := util.BuildTikuHashCandidates(row.question, nil, -1)[0]
		if err := db.Create(&entity.Tiku{Question: row.question, Type: -1, Options: string(encodedOptions), Answer: string(encodedAnswer), Hash: hash, SourceName: "AI 搜索"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	usage, result := searchUsageViaTool(t, NewDBTikuSearchTool(db), question, -1, nil)

	if result.MatchKind != "fuzzy" {
		t.Fatalf("前置条件不成立：本条用例必须走 fuzzy 路径，实际 matchKind=%q（result=%+v）", result.MatchKind, result)
	}
	if !result.Ambiguous {
		t.Fatalf("前置条件不成立：构造样本本应产生歧义，实际 result.Ambiguous=false（候选=%+v）", result.Candidates)
	}
	if !usage.Ambiguous {
		t.Fatalf("缺陷复现：result.Ambiguous=%t 但 usage.Ambiguous=%t（usage=%+v）", result.Ambiguous, usage.Ambiguous, usage)
	}
}

// 用例 2：模糊匹配但前两名分差 >= gap、且答案唯一 ⇒ usage.ambiguous 必须为 false。
//
// 这条是「非默认值」的反向钉子：如果实现把 Ambiguous 硬编码成 true，
// 它也会红。同时它证明 usage 里的值真的来自 result，不是常量。
func TestAITikuSearchCallUsageKeepsAmbiguousFalseOnClearWinner(t *testing.T) {
	db := newTikuSearchTestDB(t)
	// 题干与第一条记录几乎同文（相似度接近 1），第二条只共享极少量字词
	// （相似度低于 aiTikuMinScore=0.72 被过滤掉）⇒ 只剩 1 个候选
	// ⇒ 既不触发「分差 < gap」也不触发「答案集合不唯一」。
	//
	// ⚠️ 同用例 1：题干不能与候选题干哈希相同，否则会先命中 exact。
	question := "说明下列哪一项属于可再生能源"
	if err := createTikuRow(db, "下列哪一项属于可再生能源？", []string{"风能"}, -1); err != nil {
		t.Fatal(err)
	}
	if err := createTikuRow(db, "完全无关的另一道题目内容", []string{"42"}, -1); err != nil {
		t.Fatal(err)
	}

	usage, result := searchUsageViaTool(t, NewDBTikuSearchTool(db), question, -1, nil)

	if result.MatchKind != "fuzzy" {
		t.Fatalf("前置条件不成立：本条用例必须走 fuzzy 路径，实际 matchKind=%q", result.MatchKind)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("前置条件不成立：期望只剩 1 个候选（第二个相似度低于阈值被过滤），实际 %d 个：%+v", len(result.Candidates), result.Candidates)
	}
	if result.Ambiguous {
		t.Fatalf("前置条件不成立：单一候选不应被判为歧义，实际 result.Ambiguous=true")
	}
	// 反向钉子：若实现把 Ambiguous 硬编码成 true，这条会红。
	if usage.Ambiguous {
		t.Fatalf("usage 里的 ambiguous 应为 false，实际 true（usage=%+v）", usage)
	}
}

// 用例 3：exact 命中 ⇒ usage.ambiguous 必须为 false。
//
// 很多人会漏掉这条：tiku_search.go 的歧义判定整段挂在 `kind == "fuzzy"` 条件下，
// exact 路径根本不会置位。但「不会置位」是结果侧的事实，usage 侧要靠透传语句
// 把 false 带过去 —— 若实现只在 fuzzy 分支赋值，exact 路径会留下零值 false，
// 表面上对，实际是「没写」而不是「写成 false」。这条用例保证两条路径都显式覆盖。
func TestAITikuSearchCallUsageReportsAmbiguousFalseOnExactHit(t *testing.T) {
	db := newTikuSearchTestDB(t)
	question := "水的化学式是什么？"
	options := []string{"H2O", "CO2", "NaCl"}
	encodedOptions, _ := json.Marshal(options)
	encodedAnswer, _ := json.Marshal([]string{"H2O"})
	hash := util.BuildTikuHashCandidates(question, options, 0)[0]
	if err := db.Create(&entity.Tiku{Question: question, Type: 0, Options: string(encodedOptions), Answer: string(encodedAnswer), Hash: hash, SourceName: "AI 搜索"}).Error; err != nil {
		t.Fatal(err)
	}

	usage, result := searchUsageViaTool(t, NewDBTikuSearchTool(db), question, 0, options)

	if result.MatchKind != "exact" {
		t.Fatalf("前置条件不成立：本条用例必须走 exact 路径，实际 matchKind=%q", result.MatchKind)
	}
	if result.Ambiguous {
		t.Fatalf("前置条件不成立：exact 命中不应被判为歧义，实际 result.Ambiguous=true")
	}
	if usage.Ambiguous {
		t.Fatalf("exact 路径的 usage.ambiguous 应为 false，实际 true（usage=%+v）", usage)
	}
	// 额外钉住：exact 路径的 usage 必须是 success 且有 matchKind，
	// 否则「ambiguous=false」可能只是这条记录根本没被写完整。
	if usage.Status != "success" || usage.MatchKind != "exact" {
		t.Fatalf("exact 路径 usage 未写完整：status=%q matchKind=%q", usage.Status, usage.MatchKind)
	}
}

// createTikuRow 往题库插一行（fuzzy 场景专用，不带 options）。
func createTikuRow(db *gorm.DB, question string, answer []string, qType int) error {
	encodedOptions, _ := json.Marshal([]string{})
	encodedAnswer, _ := json.Marshal(answer)
	hash := util.BuildTikuHashCandidates(question, nil, qType)[0]
	return db.Create(&entity.Tiku{Question: question, Type: int32(qType), Options: string(encodedOptions), Answer: string(encodedAnswer), Hash: hash, SourceName: "AI 搜索"}).Error
}

// 错误路径不得被写成「不模糊」。
//
// 这条防的是一个很容易犯的错：如果把 callUsage.Ambiguous 的赋值放到 finish()
// 里（而不是成功路径上），错误路径也会写一次 false，运维会误以为
// 「查过、不模糊」。本用例要求错误路径的记录 status=error 且 ambiguous=false
// —— 后者是零值，语义由 status 区分，绝不能是「成功且不模糊」。
func TestAITikuSearchCallUsageErrorPathDoesNotClaimNonAmbiguous(t *testing.T) {
	client := &ChatGptClient{TikuSearchTool: nil} // 工具不可用 ⇒ 走早退错误路径
	web, local := 0, 0
	usage := AISearchUsage{}
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "tiku_search", Arguments: `{"question":"任意题干","type":-1,"options":[]}`}}
	if _, abort := client.executeAITikuSearchTool(context.Background(), call, &web, &local, 2, map[string]string{}, nil, &usage); abort {
		t.Fatal("工具不可用不应 abort")
	}
	if len(usage.TikuSearchCalls) != 1 {
		t.Fatalf("错误路径必须留下一条记录，实际 %d 条", len(usage.TikuSearchCalls))
	}
	call0 := usage.TikuSearchCalls[0]
	if call0.Status != "error" || call0.Error == "" {
		t.Fatalf("错误路径应记 status=error 且带 error 文案，实际 status=%q error=%q", call0.Status, call0.Error)
	}
	if call0.Ambiguous {
		t.Fatalf("错误路径不得声称有歧义，实际 ambiguous=true（%+v）", call0)
	}
}
