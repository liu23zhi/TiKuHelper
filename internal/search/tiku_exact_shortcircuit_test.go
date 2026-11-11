package search

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// P0-3 的回归钉子：exact 池里的**不可用行**不得让 fuzzy 召回一起丢掉。
//
// 缺陷形态（t67 实测 + t74 复现）：
//
//	exactRows 只要返回**非空**，SearchTiku 就不再调用 fuzzyRows
//	（tiku_search.go:86-93）。但那个非空池可能是**全部不可用**的：
//	  - Answer 是空数组（题库导入时参考答案缺失）
//	  - Type 与请求不兼容
//	  - 选项集合冲突
//	这些行随后在 L98-109 的过滤里被逐个 `continue` 掉，ranked 变空，
//	最终返回 MatchKind="none"，而**相似度达标（0.80 ≥ 0.72）的 fuzzy 候选
//	根本没被查过**。
//
// 判据：同一条 fuzzy 候选，有污染行时返回 none、无污染行时返回 fuzzy
// —— 两条同时成立才说明缺陷真实存在且可被本例捕获。

// newIsolatedTikuDB 每次返回**独立**的内存库。
//
// 不能用 newTikuSearchTestDB：它用 `t.Name()` 作库名，同一个测试函数内
// 两次调用会共享同一个内存库（`cache=shared`），第二次建行必然撞唯一索引。
func newIsolatedTikuDB(t *testing.T, tag string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"-"+tag+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.Tiku{}, &entity.TikuAnswerEvidence{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// createTiku 插入一条题库行，返回它。
func createTiku(t *testing.T, db *gorm.DB, question string, questionType int, answer, courseName, sourceName string) entity.Tiku {
	t.Helper()
	hash := util.BuildTikuHashCandidates(question, nil, questionType)[0]
	row := entity.Tiku{
		Question: question, Type: int32(questionType), Options: "[]", Answer: answer,
		Hash: hash, CourseName: courseName, SourceName: sourceName,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create tiku %q: %v", question, err)
	}
	return row
}

const (
	p03Query = "光合作用的产物是什么"
	p03Near  = "光合作用的产物主要是什么"
)

// TestExactPoolUnusableRowsDoNotStarveFuzzy 是本 PR 的主复现用例（测试要求第 3 组）。
//
// 前置判别力断言（两次），缺一这条用例就是假钉子：
//  1. 污染行必须**真的**被 exactRows 命中（否则短路没被触发，测不到缺陷）；
//  2. 该 fuzzy 候选的裸相似度必须 ≥ aiTikuMinScore（否则它本来就不该被返回，
//     "对照组返回 fuzzy" 这个前提不成立）。
func TestExactPoolUnusableRowsDoNotStarveFuzzy(t *testing.T) {
	// —— 前置断言 1：污染行确实进 exact 池 ——
	poisonDB := newIsolatedTikuDB(t, "poison-probe")
	createTiku(t, poisonDB, p03Query, 0, "[]", "c1", "no-answer")
	poisonExact, err := NewDBTikuSearchTool(poisonDB).exactRows(poisonDB, model.SearchRequest{Question: p03Query, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(poisonExact) == 0 {
		t.Fatalf("前置断言失败：污染行的 hash 没有被 exactRows 命中，本用例无判别力")
	}

	// —— 前置断言 2：fuzzy 候选裸相似度达标 ——
	if sim := tikuQuestionSimilarity(p03Query, p03Near); sim < aiTikuMinScore {
		t.Fatalf("前置断言失败：fuzzy 候选相似度 %.4f < 阈值 %.2f，对照组前提不成立", sim, aiTikuMinScore)
	}

	// —— 对照组：库里只有 fuzzy 候选 → 必须返回它 ——
	cleanDB := newIsolatedTikuDB(t, "clean")
	createTiku(t, cleanDB, p03Near, 0, `["氧气"]`, "c2", "good")
	clean, err := NewDBTikuSearchTool(cleanDB).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if clean.MatchKind != "fuzzy" || clean.CandidateCount != 1 {
		t.Fatalf("对照组前提失败：无污染行时应返回 1 条 fuzzy，got kind=%q count=%d", clean.MatchKind, clean.CandidateCount)
	}

	// —— 实验组：同一个 fuzzy 候选 + 一条不可用 exact 行 ——
	poisonedDB := newIsolatedTikuDB(t, "poisoned")
	createTiku(t, poisonedDB, p03Query, 0, "[]", "c1", "no-answer")
	createTiku(t, poisonedDB, p03Near, 0, `["氧气"]`, "c2", "good")
	poisoned, err := NewDBTikuSearchTool(poisonedDB).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
	if err != nil {
		t.Fatal(err)
	}

	// 核心断言：不可用的 exact 行不得让可用 fuzzy 候选一起消失。
	if poisoned.CandidateCount == 0 {
		t.Fatalf("exact 池被不可用行占满后，相似度 %.4f 的 fuzzy 候选被丢弃（缺陷复现）：kind=%q",
			tikuQuestionSimilarity(p03Query, p03Near), poisoned.MatchKind)
	}
	if poisoned.MatchKind != "fuzzy" {
		t.Fatalf("降级到 fuzzy 后 MatchKind 应为 \"fuzzy\"，got %q", poisoned.MatchKind)
	}
	if got := poisoned.Candidates[0].Answers; len(got) != 1 || got[0] != "氧气" {
		t.Fatalf("应返回 fuzzy 候选的答案 [氧气]，got %v", got)
	}
}

// TestExactPoolRejectsUnusableRows 覆盖测试要求第 1 组与第 4 组：
// 不可用的 exact 行不得作为候选返回；混合有效/不可用时只返回有效的。
func TestExactPoolRejectsUnusableRows(t *testing.T) {
	t.Run("无答案的 exact 行不得返回", func(t *testing.T) {
		db := newIsolatedTikuDB(t, "empty-answer")
		createTiku(t, db, p03Query, 0, "[]", "c1", "no-answer")

		// 前置断言：这一行确实进 exact 池（否则测不到过滤逻辑）。
		rows, err := NewDBTikuSearchTool(db).exactRows(db, model.SearchRequest{Question: p03Query, Type: 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatalf("前置断言失败：无答案行的 hash 未被 exactRows 命中")
		}

		result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
		if err != nil {
			t.Fatal(err)
		}
		if result.CandidateCount != 0 {
			t.Fatalf("无答案的题目不得作为候选返回，got %+v", result.Candidates)
		}
	})

	t.Run("类型不兼容的 exact 行不得返回", func(t *testing.T) {
		db := newIsolatedTikuDB(t, "type-mismatch")
		// 存 type=4，查 type=0 —— compatibleQuestionType 会拒。
		createTiku(t, db, p03Query, 4, `["某答案"]`, "c1", "wrong-type")

		rows, err := NewDBTikuSearchTool(db).exactRows(db, model.SearchRequest{Question: p03Query, Type: 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			t.Fatalf("前置断言失败：类型不兼容行的 hash 未被 exactRows 命中")
		}

		result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
		if err != nil {
			t.Fatal(err)
		}
		if result.CandidateCount != 0 {
			t.Fatalf("类型不兼容的题目不得作为候选返回，got %+v", result.Candidates)
		}
	})

	t.Run("有效与不可用混合时只返回有效的", func(t *testing.T) {
		db := newIsolatedTikuDB(t, "mixed")
		// 唯一索引是 (hash, course_name)，因此同一题面 + 不同 course_name 可造出多条 exact 命中。
		//
		// 注意这里**只能**用「无答案」来造不可用行，不能用「类型不兼容」：
		// questionType 参与 hash 计算（util.BuildTikuHash 把 strconv.Itoa(type) 拼进去），
		// 所以 type=4 的行 hash 与 type=0 不同，根本不会进 exact 池 —— 那种写法
		// 会变成一条无效的前置断言（首版踩过，断言直接报 got 2）。
		createTiku(t, db, p03Query, 0, "[]", "c1", "no-answer")
		createTiku(t, db, p03Query, 0, `["氧气"]`, "c2", "good")

		rows, err := NewDBTikuSearchTool(db).exactRows(db, model.SearchRequest{Question: p03Query, Type: 0})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("前置断言失败：应有 2 条 exact 命中（1 条无答案 + 1 条有效），got %d", len(rows))
		}

		result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
		if err != nil {
			t.Fatal(err)
		}
		if result.CandidateCount != 1 {
			t.Fatalf("混合场景应只返回 1 条有效候选，got %d：%+v", result.CandidateCount, result.Candidates)
		}
		if result.Candidates[0].Source != "good" {
			t.Fatalf("应返回来源为 good 的那条，got %q", result.Candidates[0].Source)
		}
		if result.MatchKind != "exact" {
			t.Fatalf("有效 exact 命中存在时应保持 MatchKind=exact，got %q", result.MatchKind)
		}
	})
}

// TestExactPoolValidHitKeepsExactKind 覆盖测试要求第 2 组：
// 有效的 exact 命中必须正常返回，且 MatchKind 仍是 exact、score 仍是 1.000。
//
// 这条同时防止修法走偏成「exact 之后无条件跑 fuzzy」—— 那会让正常题的
// MatchKind 漂移成 fuzzy、score 掉下 1.000，属于对既有行为的破坏。
func TestExactPoolValidHitKeepsExactKind(t *testing.T) {
	db := newIsolatedTikuDB(t, "valid-exact")
	createTiku(t, db, p03Query, 0, `["氧气"]`, "c1", "good")

	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: p03Query, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchKind != "exact" {
		t.Fatalf("有效 exact 命中的 MatchKind 必须是 exact，got %q", result.MatchKind)
	}
	if result.CandidateCount != 1 {
		t.Fatalf("应返回 1 条候选，got %d", result.CandidateCount)
	}
	if result.Candidates[0].Score != 1.0 {
		t.Fatalf("exact 命中的 score 必须是 1.000，got %v", result.Candidates[0].Score)
	}
}

// TestHasUsableTikuRowMirrorsMainFilter 钉住「可用性判定」与「主过滤循环」逐条一致。
//
// 为什么必须有这一条（t79 的核心目的）：
//
//	hasUsableTikuRow 存在的全部意义就是消除「判定可用性」与「实际采用」两处的漂移。
//	它必须与 SearchTiku 主循环的三个 continue 条件逐条对齐，否则它自己就是漂移源。
//	P0-1（#14）合入时主循环新增了 compatibleNegation —— 当时本函数没跟上，
//	本用例把两者钉在一起，以后再有人往主循环加条件而忘了这里，就会红。
//
// ⚠️ 为什么用**伪造 hash** 构造，而不是自然数据：
//
//	exact 路径走 hash 精确匹配，而 hash 由 util.BuildTikuHash 基于
//	FormatString(question) 计算。FormatString 不做否定词的等价归一
//	（「不」字原样保留），因此：
//	    相反极性的题干 → 归一化后仍不同 → hash 必然不同 → 天然进不了同一个 exact 池。
//	实测确认（t79）：
//	    BuildTikuHashCandidates("下列说法不正确的是")[0] = ca16d728…
//	    BuildTikuHashCandidates("下列说法正确的是")[0]   = 3daa2d74…
//	    「下列属于生物的是」vs「下列不属于生物的是」同样无交集。
//
//	所以 compatibleNegation 在 exact 路径上**自然可达性为零**，它挡的是
//	「hash 与 question 不一致」的异常行（迁移错误 / 手工改库 / 唯一索引被绕过）。
//	为了验证这条防线确实在起作用，这里**显式伪造** hash 让相反题落进 exact 池 ——
//	这是唯一能触达该分支的构造方式，不是自然流量形态。
func TestHasUsableTikuRowMirrorsMainFilter(t *testing.T) {
	const negatedQuery = "下列说法不正确的是"
	const positiveOpposite = "下列说法正确的是"

	// —— 前置断言 1：两者极性确实相反，否则本用例测不到否定条件 ——
	if !tikuQuestionNegated(negatedQuery) {
		t.Fatalf("前置断言失败：%q 应被判为否定问法", negatedQuery)
	}
	if tikuQuestionNegated(positiveOpposite) {
		t.Fatalf("前置断言失败：%q 应被判为正向问法", positiveOpposite)
	}
	if compatibleNegation(negatedQuery, positiveOpposite) {
		t.Fatalf("前置断言失败：相反极性必须不兼容")
	}

	// —— 前置断言 2：自然状态下两者 hash 不同（说明本用例必须伪造 hash）——
	if util.BuildTikuHashCandidates(negatedQuery, nil, 0)[0] == util.BuildTikuHashCandidates(positiveOpposite, nil, 0)[0] {
		t.Fatalf("前置假设变化：相反极性题干的 hash 竟然相同，请重新评估本用例的构造方式")
	}

	// —— 构造：把正向题的 hash 伪造成 query 的 hash，让它落进 exact 池 ——
	db := newIsolatedTikuDB(t, "negation-exact")
	queryHash := util.BuildTikuHashCandidates(negatedQuery, nil, 0)[0]
	forged := entity.Tiku{
		Question: positiveOpposite, Type: 0, Options: "[]",
		Answer: `["相反题的答案"]`, Hash: queryHash, CourseName: "forged", SourceName: "forged",
	}
	if err := db.Create(&forged).Error; err != nil {
		t.Fatal(err)
	}

	// —— 前置断言 3：伪造行确实被 exactRows 命中（否则短路没被触发）——
	tool := NewDBTikuSearchTool(db)
	rows, err := tool.exactRows(db, model.SearchRequest{Question: negatedQuery, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Question != positiveOpposite {
		t.Fatalf("前置断言失败：伪造行未被 exactRows 命中，rows=%+v", rows)
	}

	// —— 核心断言 1（函数级）：hasUsableTikuRow 必须拒掉这条相反行 ——
	if hasUsableTikuRow(rows, model.SearchRequest{Question: negatedQuery, Type: 0}) {
		t.Fatalf("hasUsableTikuRow 把相反极性的行判成了可用 —— 说明它漏了 compatibleNegation，" +
			"与主循环条件漂移了")
	}

	// —— 核心断言 2（函数级）：同一条行，把题干换成同极性后必须判可用 ——
	// 这是防「条件写反/恒假」的对照：若实现退化成无条件返回 false，本断言会红。
	samePolarity := []tikuCandidateRow{{
		Question: negatedQuery, Type: 0, Options: "[]", Answer: `["某答案"]`, Hash: queryHash,
	}}
	if !hasUsableTikuRow(samePolarity, model.SearchRequest{Question: negatedQuery, Type: 0}) {
		t.Fatalf("同极性行必须判为可用 —— 若此处失败说明否定条件写成了无条件拒绝")
	}
}

// TestExactPoolSkipsOppositePolarityAndFallsBackToFuzzy 是上面那条的端到端对照。
//
// 场景：exact 池里只有一条**极性相反**的伪造行，另有一条同极性 fuzzy 可用候选。
// 期望：返回 fuzzy 那条（正确），而不是 none，也不是 exact 那条相反题。
//
// 若 hasUsableTikuRow 漏掉 compatibleNegation → 相反行被当成可用 exact →
// 短路生效 → 返回那条错答案（MatchKind=exact、答案=相反题的答案）。
func TestExactPoolSkipsOppositePolarityAndFallsBackToFuzzy(t *testing.T) {
	const negatedQuery = "下列说法不正确的是"
	const positiveOpposite = "下列说法正确的是"
	const samePolarityNear = "下列说法不正确的一项是"

	// 前置断言 A：fuzzy 候选必须同极性且相似度达标。
	if !tikuQuestionNegated(samePolarityNear) {
		t.Fatalf("前置断言失败：%q 应为否定问法", samePolarityNear)
	}
	if !compatibleNegation(negatedQuery, samePolarityNear) {
		t.Fatalf("前置断言失败：fuzzy 候选应与 query 同极性")
	}
	if sim := tikuQuestionSimilarity(negatedQuery, samePolarityNear); sim < aiTikuMinScore {
		t.Fatalf("前置断言失败：fuzzy 候选相似度 %.4f < 阈值 %.2f，会因阈值被拒而非因否定条件",
			sim, aiTikuMinScore)
	}

	db := newIsolatedTikuDB(t, "opposite-exact")
	// 伪造：反向题占用 query 的 hash，落进 exact 池。
	queryHash := util.BuildTikuHashCandidates(negatedQuery, nil, 0)[0]
	if err := db.Create(&entity.Tiku{
		Question: positiveOpposite, Type: 0, Options: "[]",
		Answer: `["相反题的答案"]`, Hash: queryHash, CourseName: "forged", SourceName: "forged",
	}).Error; err != nil {
		t.Fatal(err)
	}
	// fuzzy 可用候选（同极性、题面近似）。
	createTiku(t, db, samePolarityNear, 0, `["正确的答案"]`, "c2", "good")

	// 前置断言 B：伪造行确实在 exact 池里。
	rows, err := NewDBTikuSearchTool(db).exactRows(db, model.SearchRequest{Question: negatedQuery, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("前置断言失败：伪造行应为 exact 命中，got %d", len(rows))
	}

	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: negatedQuery, Type: 0})
	if err != nil {
		t.Fatal(err)
	}

	if result.CandidateCount == 0 {
		t.Fatalf("相反极性 exact 行被拒后应降级到 fuzzy，实际返回 none")
	}
	if result.MatchKind != "fuzzy" {
		t.Fatalf("应降级为 fuzzy，got %q", result.MatchKind)
	}
	if got := result.Candidates[0].Answers; len(got) != 1 || got[0] != "正确的答案" {
		t.Fatalf("应返回同极性 fuzzy 候选的答案 [正确的答案]，got %v", got)
	}
}
