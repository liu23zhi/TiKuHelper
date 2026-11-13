package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"gorm.io/gorm"
)

// hashSensitiveWordTextWithProfile 按**与生产完全相同的公式**计算 hash，
// 只是把 profile 作为参数传入，用来证明「不同 profile 得到不同键」。
//
// 公式必须与 sensitive_words.go:267 逐字节一致：
//
//	sha256(profile + "\x00" + text) 的 hex
//
// 这不是"复制一份实现然后自比"——TestSensitiveWordCacheHashSeparatesProfiles
// 会同时断言「本函数算出的当前 profile 结果 == 真实函数的结果」，
// 所以公式一旦漂移，那条断言先红。
func hashSensitiveWordTextWithProfile(profile, text string) string {
	sum := sha256.Sum256([]byte(profile + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// newSensitiveWordCacheListDB 建一个只含缓存表的测试库。
func newSensitiveWordCacheListDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SensitiveWordCache{}); err != nil {
		t.Fatalf("migrate cache: %v", err)
	}
	return db
}

// seedSensitiveWordCacheRow 按"上游结论"写一行等价缓存。
//
// 用真实的 saveSensitiveWordCacheWithDB 写入而不是 db.Create：
// 这样写入口径（OnConflict 列集合、CheckedAt 赋值）与生产路径一致，
// 列表读出来的东西就是生产会出现的东西。
func seedSensitiveWordCacheRow(t *testing.T, db *gorm.DB, text string, sensitive bool, masked, status, errText string, attempts int) string {
	t.Helper()
	hash := hashSensitiveWordText(text)
	words := "[]"
	if sensitive {
		words = `["敏感"]`
	}
	saveSensitiveWordCacheWithDB(context.Background(), db, entity.SensitiveWordCache{
		TextHash: hash, Sensitive: sensitive, Words: words, MaskedText: masked,
		APIStatus: status, Error: errText, AttemptCount: attempts,
	})
	return hash
}

// TestSensitiveWordCacheListFiltersByThreeStates 是判据 ① 的钉子。
//
// 三态必须**互斥且完整**：同一批数据按 hit / clean / failed 分别筛出来的
// 条数之和，必须等于不筛选时的总数。这比"每个状态各有一条"更强 ——
// 它同时抓「条件写漏一个状态」和「两个状态重叠」。
func TestSensitiveWordCacheListFiltersByThreeStates(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	hitHash := seedSensitiveWordCacheRow(t, db, "命中的题目", true, "**的题目", "forbidden", "", 0)
	cleanHash := seedSensitiveWordCacheRow(t, db, "干净的题目", false, "干净的题目", "clean", "", 0)
	failedHash := seedSensitiveWordCacheRow(t, db, "失败的题目", false, "", "", "upstream 500", 1)

	all, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 3 {
		t.Fatalf("不筛选应返回 3 条，实际 %d", all.Total)
	}

	cases := []struct {
		state string
		want  string
	}{
		{SensitiveWordCacheStateHit, hitHash},
		{SensitiveWordCacheStateClean, cleanHash},
		{SensitiveWordCacheStateFailed, failedHash},
	}
	sum := int64(0)
	for _, tc := range cases {
		result, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{State: tc.state}, 1, 20)
		if err != nil {
			t.Fatalf("list state=%s: %v", tc.state, err)
		}
		if result.Total != 1 || len(result.Data) != 1 {
			t.Fatalf("state=%s 应恰好 1 条，实际 total=%d len=%d", tc.state, result.Total, len(result.Data))
		}
		if result.Data[0].TextHash != tc.want {
			t.Fatalf("state=%s 取到了错误的行：got=%s want=%s", tc.state, result.Data[0].TextHash, tc.want)
		}
		if result.Data[0].State != tc.state {
			t.Fatalf("state=%s 行的 State 字段应为 %s，实际 %s", tc.state, tc.state, result.Data[0].State)
		}
		sum += result.Total
	}
	if sum != all.Total {
		t.Fatalf("三态互斥且完整：hit/clean/failed 三态之和 %d 应等于总数 %d", sum, all.Total)
	}

	// 三态计数也必须与筛选口径一致（两者是两套代码，容易漂移）。
	if all.States.Hit != 1 || all.States.Clean != 1 || all.States.Failed != 1 {
		t.Fatalf("三态计数与筛选口径漂移：%+v", all.States)
	}
}

// TestSensitiveWordCacheListRejectsUnknownState 保证非法状态是 400 而不是静默忽略。
//
// 静默忽略的后果：管理员以为筛了，实际在看全量 —— 「看起来在筛、其实没筛」。
func TestSensitiveWordCacheListRejectsUnknownState(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	seedSensitiveWordCacheRow(t, db, "题目", true, "**", "forbidden", "", 0)
	for _, state := range []string{"bogus", "forbiden", "hits", "failed hit"} {
		if _, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{State: state}, 1, 20); err == nil {
			t.Fatalf("非法状态 %q 必须报错而不是静默忽略", state)
		}
	}
	// 大小写与空白归一化后应被接受。
	result, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{State: "  HIT "}, 1, 20)
	if err != nil {
		t.Fatalf("归一化后应接受 HIT：%v", err)
	}
	if result.Total != 1 {
		t.Fatalf("归一化后应筛出 1 条，实际 %d", result.Total)
	}
}

// TestSensitiveWordCacheListNeverExposesOriginalText 是判据 ② 的**后端**断言。
//
// 需求原话：MaskedText 可看但**原文不可看**，且必须"不是靠前端隐藏"。
// 这里从两个方向钉死：
//  1. 列表结构体序列化后的 JSON 里，**没有任何字段**等于原文；
//  2. 缓存表里就查不到原文列（按设计不存），所以这不是前端藏起来的问题。
//
// 变异方向：往 SensitiveWordCacheListItem 加一个 `Question`/`Text` 字段并
// 赋值原文 → 这里的 JSON 检查必红。注意那条变异**不可能**发生，
// 因为原文在库里根本不存在 —— 这正是本用例要证明的事。
func TestSensitiveWordCacheListNeverExposesOriginalText(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	const original = "这是一段足够长的原始题目文本用于验证原文绝不外泄"
	const masked = "这是一段足够长的************用于验证原文绝不外泄"
	hash := seedSensitiveWordCacheRow(t, db, original, true, masked, "forbidden", "", 0)

	// 方向 1：数据库层面根本没有存原文的列。
	if db.Migrator().HasColumn(&entity.SensitiveWordCache{}, "text") {
		t.Fatalf("缓存表不应存在存原文的 text 列")
	}
	for _, column := range []string{"question", "answer", "original_text", "raw_text"} {
		if db.Migrator().HasColumn(&entity.SensitiveWordCache{}, column) {
			t.Fatalf("缓存表不应存在 %s 列", column)
		}
	}
	var raw entity.SensitiveWordCache
	if err := db.Where("text_hash = ?", hash).Take(&raw).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if strings.Contains(raw.MaskedText, original) {
		t.Fatalf("MaskedText 里不应包含原文")
	}

	// 方向 2：下发结构体里没有任何字段带原文。
	result, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(result.Data))
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), original) {
		t.Fatalf("下发 JSON 里出现了原文：%s", string(payload))
	}
	if !strings.Contains(string(payload), masked) {
		t.Fatalf("下发 JSON 里应保留 MaskedText 供排障：%s", string(payload))
	}
	// 逐字段再确认一次：结构体不得出现原文类字段名。
	for _, forbidden := range []string{`"question"`, `"answer"`, `"originalText"`, `"rawText"`, `"text":`} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("下发 JSON 不应包含字段 %s：%s", forbidden, string(payload))
		}
	}
}

// TestSensitiveWordCacheSecondCheckMakesNoUpstreamCall 是判据 ③ 的钉子。
//
// 「同一文本二次检测不产生上游调用」—— 这条本来就由 service 层实现
// （loadSensitiveWordCacheWithDB 命中即返回）。这里把它钉成**行为级**用例：
// 第一次外呼 1 次，第二次 0 次，而且两次结论一致。
//
// 注意作用域：这里用同一个 db + 同一段文本，走 RunSensitiveWordCheckWithDB，
// 与生产路径一致（不是手搓 struct 自比）。
func TestSensitiveWordCacheSecondCheckMakesNoUpstreamCall(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	calls := 0
	detector := func(_ context.Context, _ string) (uapi.ProfanityCheckResponse, error) {
		calls++
		return forbiddenResponse("敏感"), nil
	}

	first := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "同一道题", "")
	if calls != 1 {
		t.Fatalf("首次检测应外呼 1 次（题目），实际 %d 次", calls)
	}
	second := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "同一道题", "")
	if calls != 1 {
		t.Fatalf("同一文本二次检测不得再外呼，实际累计 %d 次", calls)
	}
	if !first.Flag || !second.Flag {
		t.Fatalf("两次结论都必须为命中：first=%v second=%v", first.Flag, second.Flag)
	}
	if second.Words != first.Words {
		t.Fatalf("命中词必须一致：first=%s second=%s", first.Words, second.Words)
	}
}

// TestSensitiveWordCacheHashSeparatesProfiles 是判据 ④ 的钉子。
//
// 「不同 profile 的同一文本必须是两个不同的键」。
// profile 是哈希域分隔前缀（sensitive_words.go:248）：换检测口径时改它
// 等于整体换一套键，避免新旧语义混用同一批缓存行。
//
// 变异方向：把 textHash 的公式从 sha256(profile + "\x00" + text) 改成
// sha256(text) → belowBothDifferent 会变成相等 → 本用例必红。
//
// ⚠️ 这里**不能**用"测试里自己算一遍 sha256 然后自比"的写法：
// 那样真实实现被变异时测试自带的那份副本照样算出旧值，用例永远绿
// （典型的假钉子）。所以断言必须锚在**真实函数**上：
//   - 用真实 hashSensitiveWordText 产出的键，去查真实写入的缓存行；
//   - 用 sensitiveWordCacheProfile 常量本身证明"profile 参与了计算"
//     （改 profile 常量 → 键整体变）。
func TestSensitiveWordCacheHashSeparatesProfiles(t *testing.T) {
	const text = "完全相同的文本"

	// ① 真实函数必须对同一文本稳定（基本性质）。
	first := hashSensitiveWordText(text)
	if first != hashSensitiveWordText(text) {
		t.Fatalf("同文本的真实 hash 必须稳定")
	}
	// ② 真实函数必须对文本敏感（防"忽略入参"的坏实现）。
	if first == hashSensitiveWordText(text+"改") {
		t.Fatalf("不同文本必须得到不同 hash")
	}
	// ③ 真实 hash 必须等于「当前 profile + NUL + 文本」的 sha256 ——
	//    这条把 profile 域分隔的公式钉死：把公式改成 sha256(text) 时，
	//    右边仍按 profile+NUL+text 计算，必然不相等。
	if want := hashSensitiveWordTextWithProfile(sensitiveWordCacheProfile, text); first != want {
		t.Fatalf("真实 hash 必须使用 profile 域分隔公式，got=%s want=%s", first, want)
	}
	// ④ "不同 profile 的同一文本 → 不同键"：直接对比两套公式的产物。
	//    注意要**走真实函数**才有意义 —— 真实函数固定用 sensitiveWordCacheProfile，
	//    所以这里对比「真实函数」与「换一个 profile 的公式」，
	//    再额外断言两者都不为空且长度 64，避免拿两个空串比。
	other := hashSensitiveWordTextWithProfile("uapi-profanity-plain-v2", text)
	if first == other {
		t.Fatalf("不同 profile 的同一文本必须得到不同 hash（否则换口径会复用旧缓存）")
	}
	if len(first) != 64 || len(other) != 64 {
		t.Fatalf("hash 必须是 64 位 hex：first=%d other=%d", len(first), len(other))
	}
}

// TestSensitiveWordCacheStateClassifiesDirtyRowAsFailed 钉住三态判定的**优先级**。
//
// 为什么需要这条：正常写入路径不会产生「Error 非空 且 Sensitive=true」的行
// （resolve 的失败分支固定写 Sensitive=false），所以只造正常行的话，
// 把 classify 里两个分支对调（先判 Sensitive 再判 Error）**没有任何用例会红** ——
// 这是一个真实的覆盖缺口，实测变异后两个包全绿。
//
// 判据：脏行必须归 failed。理由：错误的答案标记比"漏记一次失败"代价大得多，
// 一行既有失败原因又被打上命中，只有在数据被外部改坏时才会出现，
// 此时唯一安全的解读是"这次检测没成功"。
func TestSensitiveWordCacheStateClassifiesDirtyRowAsFailed(t *testing.T) {
	// 纯函数级：优先级不能反。
	if got := classifySensitiveWordCacheState(true, "upstream 500"); got != SensitiveWordCacheStateFailed {
		t.Fatalf("Error 非空时必须归 failed（优先级高于 Sensitive），实际 %s", got)
	}
	if got := classifySensitiveWordCacheState(true, "   "); got != SensitiveWordCacheStateHit {
		t.Fatalf("Error 全是空白时按命中处理，实际 %s", got)
	}
	if got := classifySensitiveWordCacheState(false, ""); got != SensitiveWordCacheStateClean {
		t.Fatalf("无错误且未命中必须归 clean，实际 %s", got)
	}

	// 端到端：直接写一行脏数据（绕过 save 接口，模拟库被外部改坏），
	// 确认列表把它归到 failed，且按 failed 筛选能筛出来、按 hit 筛不出来。
	db := newSensitiveWordCacheListDB(t)
	dirtyHash := strings.Repeat("9", 64)
	if err := db.Create(&entity.SensitiveWordCache{
		TextHash: dirtyHash, Sensitive: true, Words: `["敏感"]`,
		MaskedText: "**", APIStatus: "forbidden", Error: "upstream 500", AttemptCount: 1,
	}).Error; err != nil {
		t.Fatalf("seed dirty row: %v", err)
	}
	failed, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{State: SensitiveWordCacheStateFailed}, 1, 20)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if failed.Total != 1 || failed.Data[0].State != SensitiveWordCacheStateFailed {
		t.Fatalf("脏行必须归 failed：total=%d state=%v", failed.Total, failed.Data)
	}
	hit, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{State: SensitiveWordCacheStateHit}, 1, 20)
	if err != nil {
		t.Fatalf("list hit: %v", err)
	}
	if hit.Total != 0 {
		t.Fatalf("脏行不得被算作命中：total=%d", hit.Total)
	}
}

// TestSensitiveWordCacheFiltersCombineKeywordAndRange 覆盖筛选组合与日期校验。
func TestSensitiveWordCacheFiltersCombineKeywordAndRange(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	seedSensitiveWordCacheRow(t, db, "含错误关键字的文本", false, "", "", "boom-500", 1)
	seedSensitiveWordCacheRow(t, db, "正常文本", true, "**", "forbidden", "", 0)

	byKeyword, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{Keyword: "boom-500"}, 1, 20)
	if err != nil {
		t.Fatalf("keyword: %v", err)
	}
	if byKeyword.Total != 1 || byKeyword.Data[0].State != SensitiveWordCacheStateFailed {
		t.Fatalf("按错误文案反查应命中那条失败记录：%+v", byKeyword)
	}

	// 时间区间：未来区间应为空，而不是报错。
	future := time.Now().AddDate(0, 1, 0).Format("2006-01-02")
	empty, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{CheckedFrom: future}, 1, 20)
	if err != nil {
		t.Fatalf("future range: %v", err)
	}
	if empty.Total != 0 {
		t.Fatalf("未来区间应为空，实际 %d", empty.Total)
	}

	// 区间倒置必须报错。
	if _, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{
		CheckedFrom: time.Now().Format("2006-01-02"),
		CheckedTo:   time.Now().AddDate(0, 0, -10).Format("2006-01-02"),
	}, 1, 20); err == nil {
		t.Fatalf("开始时间晚于结束时间必须报错")
	}

	// 分页：pageSize=1 时两页各一条，且不重复。
	page1, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{}, 1, 1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	page2, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{}, 2, 1)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if page1.Total != 2 || len(page1.Data) != 1 || len(page2.Data) != 1 {
		t.Fatalf("分页应各返回 1 条且总数 2：p1=%d p2=%d total=%d", len(page1.Data), len(page2.Data), page1.Total)
	}
	if page1.Data[0].TextHash == page2.Data[0].TextHash {
		t.Fatalf("两页不得返回同一行")
	}
}

// TestSensitiveWordCacheStatusCountsMatchList 保证状态接口与列表口径一致。
func TestSensitiveWordCacheStatusCountsMatchList(t *testing.T) {
	db := newSensitiveWordCacheListDB(t)
	seedSensitiveWordCacheRow(t, db, "hit", true, "**", "forbidden", "", 0)
	seedSensitiveWordCacheRow(t, db, "clean", false, "clean", "clean", "", 0)
	seedSensitiveWordCacheRow(t, db, "failed", false, "", "", "boom", 2)

	counts, latest, err := SensitiveWordCacheStatusWithDB(db)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if counts.Hit != 1 || counts.Clean != 1 || counts.Failed != 1 {
		t.Fatalf("status 三态计数不对：%+v", counts)
	}
	if latest == nil {
		t.Fatalf("有记录时 latestCheckedAt 不应为 nil")
	}
	all, err := ListSensitiveWordCacheWithDB(db, SensitiveWordCacheListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if all.States != counts {
		t.Fatalf("status 与 list 的三态计数必须一致：status=%+v list=%+v", counts, all.States)
	}
}
