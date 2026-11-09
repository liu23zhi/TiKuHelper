package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"gorm.io/gorm"
)

func newSensitiveWordTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	return db
}

// ensureSensitiveLogTable 建出带敏感词四列的 logs 表。
//
// 用裸 SQL 而不是 AutoMigrate(LogSensitive)：后者的 entity 缺少 logs 其余列，
// AutoMigrate 会按「实体声明」建表，把生产列全丢掉。
func ensureSensitiveLogTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			qid INTEGER NOT NULL DEFAULT 0,
			old_answer TEXT,
			new_answer TEXT,
			user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT,
			module TEXT,
			action INTEGER NOT NULL DEFAULT 0,
			description TEXT,
			correlation_id TEXT,
			client_ip TEXT,
			create_time datetime NOT NULL,
			sensitive_flag boolean NOT NULL DEFAULT 0,
			sensitive_words TEXT,
			sensitive_checked_at datetime,
			sensitive_check_error varchar(255)
		)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create logs table: %v", err)
		}
	}
}

func stubDetector(t *testing.T, responses map[string]uapi.ProfanityCheckResponse, errs map[string]error, calls *[]string) SensitiveWordDetector {
	t.Helper()
	return func(_ context.Context, text string) (uapi.ProfanityCheckResponse, error) {
		if calls != nil {
			*calls = append(*calls, text)
		}
		if err := errs[text]; err != nil {
			return uapi.ProfanityCheckResponse{}, err
		}
		if response, ok := responses[text]; ok {
			return response, nil
		}
		return uapi.ProfanityCheckResponse{Status: "clean"}, nil
	}
}

func forbiddenResponse(words ...string) uapi.ProfanityCheckResponse {
	return uapi.ProfanityCheckResponse{
		Status:         "forbidden",
		MaskedText:     "***",
		ForbiddenWords: words,
		RawJSON:        `{"status":"forbidden"}`,
	}
}

func TestRunSensitiveWordCheckMarksQuestionHit(t *testing.T) {
	calls := make([]string, 0, 2)
	detector := stubDetector(t, map[string]uapi.ProfanityCheckResponse{
		"题目含敏感词": forbiddenResponse("敏感词"),
	}, nil, &calls)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "题目含敏感词", "干净的答案")

	if !outcome.Flag {
		t.Fatalf("题目命中时 Flag 必须为 true，实际 %+v", outcome)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("必须分别检测题目与答案，实际 %d 组", len(outcome.Results))
	}
	if outcome.Results[0].Scope != SensitiveWordScopeQuestion || !outcome.Results[0].Forbidden {
		t.Fatalf("题目命中标记错误：%+v", outcome.Results[0])
	}
	if outcome.Results[1].Scope != SensitiveWordScopeAnswer || outcome.Results[1].Forbidden {
		t.Fatalf("答案未命中却被标记：%+v", outcome.Results[1])
	}
	if strings.TrimSpace(outcome.Error) != "" {
		t.Fatalf("成功检测不应带失败原因：%q", outcome.Error)
	}
}

func TestRunSensitiveWordCheckMarksAnswerHit(t *testing.T) {
	detector := stubDetector(t, map[string]uapi.ProfanityCheckResponse{
		"答案含敏感词": forbiddenResponse("答案敏感词", "另一个词"),
	}, nil, nil)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "干净的题目", "答案含敏感词")

	if !outcome.Flag {
		t.Fatalf("答案命中时 Flag 必须为 true，实际 %+v", outcome)
	}
	answerResult := outcome.Results[1]
	if !answerResult.Forbidden {
		t.Fatalf("答案命中标记错误：%+v", answerResult)
	}
	if len(answerResult.Words) != 2 || answerResult.Words[0] != "答案敏感词" {
		t.Fatalf("命中词必须按上游顺序保留，实际 %+v", answerResult.Words)
	}
	if outcome.Results[0].Forbidden {
		t.Fatalf("题目未命中却被标记：%+v", outcome.Results[0])
	}
}

func TestRunSensitiveWordCheckMarksBothHitsIndependently(t *testing.T) {
	detector := stubDetector(t, map[string]uapi.ProfanityCheckResponse{
		"题目敏感": forbiddenResponse("甲"),
		"答案敏感": forbiddenResponse("乙"),
	}, nil, nil)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "题目敏感", "答案敏感")

	if !outcome.Flag {
		t.Fatalf("都命中时 Flag 必须为 true")
	}
	question, answer := outcome.Results[0], outcome.Results[1]
	if !question.Forbidden || !answer.Forbidden {
		t.Fatalf("两组都必须各自标记命中：%+v", outcome.Results)
	}
	if question.Words[0] != "甲" || answer.Words[0] != "乙" {
		t.Fatalf("两组的命中词必须各自独立：%+v", outcome.Results)
	}
}

func TestRunSensitiveWordCheckNonForbiddenStatusClearsFlag(t *testing.T) {
	// 上游 status 除 forbidden 之外的任何取值都按「未命中」处理。
	for _, status := range []string{"clean", "safe", "ok", "CLEAN", ""} {
		if status == "" {
			continue // 空 status 由 ParseProfanityCheck 判为上游异常，不走这条断言。
		}
		t.Run(status, func(t *testing.T) {
			detector := func(context.Context, string) (uapi.ProfanityCheckResponse, error) {
				return uapi.ProfanityCheckResponse{Status: status, MaskedText: "原文"}, nil
			}
			outcome := RunSensitiveWordCheck(context.Background(), detector, "题目", "答案")
			if outcome.Flag {
				t.Fatalf("status=%q 不得判为命中", status)
			}
			if outcome.Results[0].Words == nil || outcome.Results[1].Words == nil {
				t.Fatalf("未命中时 Words 必须是空切片而不是 nil：%+v", outcome.Results)
			}
		})
	}
}

func TestRunSensitiveWordCheckForbiddenIsCaseInsensitiveOnStatus(t *testing.T) {
	detector := func(context.Context, string) (uapi.ProfanityCheckResponse, error) {
		return uapi.ProfanityCheckResponse{Status: "FORBIDDEN", ForbiddenWords: []string{"x"}}, nil
	}
	if outcome := RunSensitiveWordCheck(context.Background(), detector, "题目", ""); !outcome.Flag {
		t.Fatalf("status 大小写不应影响命中判定：%+v", outcome)
	}
}

func TestRunSensitiveWordCheckFailureDoesNotFlagAndDoesNotAbortOtherScope(t *testing.T) {
	// 答案检测失败，题目正常通过 → 不误标，且失败原因被记录。
	detector := stubDetector(t, map[string]uapi.ProfanityCheckResponse{
		"干净题目": {Status: "clean"},
	}, map[string]error{
		"检测失败的答案": errors.New("uapi: HTTP 500"),
	}, nil)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "干净题目", "检测失败的答案")

	if outcome.Flag {
		t.Fatalf("检测失败绝不能误标为命中：%+v", outcome)
	}
	if !strings.Contains(outcome.Error, "answer") || !strings.Contains(outcome.Error, "HTTP 500") {
		t.Fatalf("失败原因必须带 scope 与原因：%q", outcome.Error)
	}
}

func TestRunSensitiveWordCheckFailureDoesNotMaskConfirmedHit(t *testing.T) {
	// 题目命中、答案检测失败 → 已确认的命中不能被失败掩盖。
	detector := stubDetector(t, map[string]uapi.ProfanityCheckResponse{
		"敏感题目": forbiddenResponse("词"),
	}, map[string]error{
		"坏答案": errors.New("uapi: HTTP 500"),
	}, nil)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "敏感题目", "坏答案")

	if !outcome.Flag {
		t.Fatalf("已确认的命中不得被另一对象的失败掩盖：%+v", outcome)
	}
	if strings.TrimSpace(outcome.Error) == "" {
		t.Fatalf("同时必须保留失败原因供排障：%+v", outcome)
	}
}

func TestRunSensitiveWordCheckSkipsEmptyTextWithoutCallingUpstream(t *testing.T) {
	calls := make([]string, 0, 4)
	detector := stubDetector(t, nil, nil, &calls)

	outcome := RunSensitiveWordCheck(context.Background(), detector, "   ", "")

	if len(calls) != 0 {
		t.Fatalf("空文本不该外呼，实际调用 %d 次：%v", len(calls), calls)
	}
	if outcome.Flag {
		t.Fatalf("空文本不得判为命中")
	}
	for _, result := range outcome.Results {
		if result.Status != "skipped_empty" {
			t.Fatalf("空文本应对应 skipped_empty，实际 %+v", result)
		}
	}
}

func TestRunSensitiveWordCheckWithoutDetectorReportsUnconfigured(t *testing.T) {
	outcome := RunSensitiveWordCheck(context.Background(), nil, "题目", "答案")

	if outcome.Flag {
		t.Fatalf("无检测通道时不得标记")
	}
	if !strings.Contains(outcome.Error, SensitiveWordScopeQuestion) {
		t.Fatalf("必须说明未配置原因：%q", outcome.Error)
	}
}

func TestRunSensitiveWordCheckTruncatesOnlyTheSubmittedCopy(t *testing.T) {
	// 截断只作用于送检副本；题面原文与返回值都不变。
	huge := strings.Repeat("敏", sensitiveWordQuestionMaxBytes/3+50) // 中文 3 字节/字
	var got string
	detector := func(_ context.Context, text string) (uapi.ProfanityCheckResponse, error) {
		got = text
		return uapi.ProfanityCheckResponse{Status: "clean"}, nil
	}

	outcome := RunSensitiveWordCheck(context.Background(), detector, huge, "")

	if len(got) > sensitiveWordQuestionMaxBytes {
		t.Fatalf("送检文本应按字节截断到 <= %d，实际 %d 字节", sensitiveWordQuestionMaxBytes, len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断后必须是合法 UTF-8")
	}
	// 中文 3 字节/字，且预算能被 3 整除，所以应恰好切在字符边界、长度接近预算。
	if len(got) != sensitiveWordQuestionMaxBytes {
		t.Fatalf("中文送检文本应恰好截到 %d 字节，实际 %d", sensitiveWordQuestionMaxBytes, len(got))
	}
	if outcome.Flag {
		t.Fatalf("截断本身不该产生命中")
	}
}

// TestSensitiveWordTextLimitsNeverExceedUpstreamCap 是**上游硬限制**的回归钉子。
//
// uapis.cn 敏感词检测接口的 `text` 字段最大 **262144 字节（256 KiB，UTF-8 计）**，
// 超限返回 HTTP 413。这是 2026-11-09 用真实密钥实测的结论（见 task_doc/51-*.md）：
//   - text 262144 字节 → 200；262145 字节 → 413
//   - 中文 87300 字符（261900 B）→ 200；87400 字符（262200 B）→ 413
//
// 历史教训：这个常量先后被估成 2000 runes 和 1000 runes，**两次都错**，
// 而且错的方向是「过度保守」—— 不报错、只静默漏检，从 2000 一路活到 1000 都没人发现。
// 所以这条断言同时钉住两件事：
//
//	① 上限不得超过上游硬限制（否则 413）；
//	② 上限不得低于安全预算（否则又变成过度保守，白白丢弃可检测内容）。
func TestSensitiveWordTextLimitsNeverExceedUpstreamCap(t *testing.T) {
	// 实测硬限制：text 字段 262144 字节。
	const upstreamTextCapBytes = 262144
	// 本项目采用的字节预算：留 ~8.6% 余量，避免边界抖动。
	const expectedBudgetBytes = 240 * 1024

	if sensitiveWordMaxTextBytes >= upstreamTextCapBytes {
		t.Fatalf("送检文本上限 %d 字节 >= 上游硬限制 %d 字节（会 413）",
			sensitiveWordMaxTextBytes, upstreamTextCapBytes)
	}
	if sensitiveWordMaxTextBytes > expectedBudgetBytes {
		t.Fatalf("送检文本上限 %d 字节超出安全余量预算 %d 字节", sensitiveWordMaxTextBytes, expectedBudgetBytes)
	}
	for name, limit := range map[string]int{
		"question": sensitiveWordQuestionMaxBytes,
		"answer":   sensitiveWordAnswerMaxBytes,
	} {
		if limit >= upstreamTextCapBytes {
			t.Fatalf("%s 送检上限 %d 字节 >= 上游硬限制 %d 字节", name, limit, upstreamTextCapBytes)
		}
		if limit <= 0 {
			t.Fatalf("%s 送检上限必须为正数，实际 %d", name, limit)
		}
	}
}

// TestSensitiveWordTextLimitsAreByteBasedNotRuneBased 钉住「上限的单位是字节」。
//
// 这是本 bug 的根因：上游按 UTF-8 字节限制，而历史实现按 rune 限制。
// 中文 3 字节/字，同样的 rune 数对应 3 倍字节数，所以 rune 预算根本管不住字节数。
// 断言方式：一段中文字符串的字节数 = 3 × rune 数，若上限按 rune 理解则必然越界。
func TestSensitiveWordTextLimitsAreByteBasedNotRuneBased(t *testing.T) {
	const chinese = "敏"
	if len(chinese) != 3 || utf8.RuneCountInString(chinese) != 1 {
		t.Fatalf("前置假设失败：中文应为 3 字节 1 rune")
	}
	// 预算内允许的最大中文字符数 = 预算字节 / 3，远小于预算本身。
	maxChineseRunes := sensitiveWordMaxTextBytes / len(chinese)
	if maxChineseRunes >= sensitiveWordMaxTextBytes {
		t.Fatalf("中文可容纳 rune 数 %d 不应 >= 字节预算 %d（说明单位搞错了）",
			maxChineseRunes, sensitiveWordMaxTextBytes)
	}
	// 用满预算的中文文本必须不越上游硬限制。
	if maxChineseRunes*len(chinese) > 262144 {
		t.Fatalf("%d 个中文字符 = %d 字节，超过上游硬限制 262144", maxChineseRunes, maxChineseRunes*len(chinese))
	}
}

// TestTruncateSensitiveWordTextWarnsWhenTruncated 证明截断**不再静默**。
//
// 送检内容被截断 = 超出预算的部分根本没被检测，里面的敏感词整段漏检。
// 这条日志是运营发现「有题被截断、可能漏检」的唯一线索，所以必须断言：
// ① 超长时确实写了 WARN，且带上 scope / 原始字节与 rune 数 / 预算 / 丢弃量；
// ② 未超长时**一条都不写**（否则每条正常题都刷 WARN，真的截断反而被淹没）。
func TestTruncateSensitiveWordTextWarnsWhenTruncated(t *testing.T) {
	original := gin.DefaultWriter
	var captured bytes.Buffer
	gin.DefaultWriter = &captured
	t.Cleanup(func() { gin.DefaultWriter = original })

	// 用 ASCII（1 字节/字符）便于精确断言字节数。
	const limit = 20
	text := strings.Repeat("a", limit+7)
	got := truncateSensitiveWordText(SensitiveWordScopeQuestion, text, limit)

	if len(got) != limit {
		t.Fatalf("送检文本应截断到 %d 字节，实际 %d", limit, len(got))
	}
	logged := captured.String()
	for _, want := range []string{
		"sensitive_word_text_truncated",
		"scope=question",
		fmt.Sprintf("originalBytes=%d", limit+7),
		fmt.Sprintf("originalRunes=%d", limit+7),
		fmt.Sprintf("limitBytes=%d", limit),
		fmt.Sprintf("truncatedBytes=%d", limit),
		"droppedBytes=7",
		"droppedRunes=7",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("截断日志缺少 %q：%q", want, logged)
		}
	}

	captured.Reset()
	if got := truncateSensitiveWordText(SensitiveWordScopeAnswer, strings.Repeat("a", limit), limit); len(got) != limit {
		t.Fatalf("恰好等于预算时不得截断")
	}
	if logged := captured.String(); logged != "" {
		t.Fatalf("未超预算时不得写截断日志，实际写了：%q", logged)
	}
}

// TestTruncateSensitiveWordTextKeepsValidUTF8 钉住「按字节切但绝不切碎字符」。
//
// 这是按字节截断最容易写错的地方：`text[:maxBytes]` 若正好落在 3 字节中文的中间，
// 会产出非法 UTF-8 —— 送上游被判 400，落库是坏字符串。
// 逐字节回退到 rune 边界后，输出必须是合法 UTF-8，且是原文的前缀。
func TestTruncateSensitiveWordTextKeepsValidUTF8(t *testing.T) {
	original := gin.DefaultWriter
	gin.DefaultWriter = io.Discard
	t.Cleanup(func() { gin.DefaultWriter = original })

	// 每个中文 3 字节。预算取「非 3 的倍数」，强制切点落在字符中间。
	for _, budget := range []int{1, 2, 4, 5, 7, 8, 10, 11, 100, 1001} {
		text := strings.Repeat("中", 400) // 1200 字节
		got := truncateSensitiveWordText(SensitiveWordScopeQuestion, text, budget)
		if !utf8.ValidString(got) {
			t.Fatalf("budget=%d 截断结果不是合法 UTF-8：%q", budget, got)
		}
		if !strings.HasPrefix(text, got) {
			t.Fatalf("budget=%d 截断结果必须是原文前缀", budget)
		}
		if len(got) > budget {
			t.Fatalf("budget=%d 截断结果 %d 字节超出预算", budget, len(got))
		}
		// 回退量必须小于一个字符的最大字节数（3），否则说明回退逻辑写错了。
		if budget-len(got) >= 3 {
			t.Fatalf("budget=%d 回退 %d 字节，超过单个字符长度", budget, budget-len(got))
		}
		if len(got)%3 != 0 {
			t.Fatalf("budget=%d 结果 %d 字节不是 3 的倍数（说明切在字符中间）", budget, len(got))
		}
	}

	// 混合宽度：ASCII + 中文 + emoji(4 字节)，逐 budget 全量扫描。
	mixed := "abc中文😀def汉字"
	for budget := 1; budget <= len(mixed); budget++ {
		got := truncateSensitiveWordText(SensitiveWordScopeAnswer, mixed, budget)
		if !utf8.ValidString(got) {
			t.Fatalf("mixed budget=%d 结果不是合法 UTF-8：%q", budget, got)
		}
		if !strings.HasPrefix(mixed, got) {
			t.Fatalf("mixed budget=%d 结果必须是原文前缀", budget)
		}
	}
}

// TestTruncateSensitiveWordTextBoundaryExact 钉住边界语义：恰好等于预算不截断。
func TestTruncateSensitiveWordTextBoundaryExact(t *testing.T) {
	original := gin.DefaultWriter
	var captured bytes.Buffer
	gin.DefaultWriter = &captured
	t.Cleanup(func() { gin.DefaultWriter = original })

	// 262144 字节纯 ASCII 远超 240KiB 预算 → 必然截断到预算。
	big := strings.Repeat("a", 262144)
	got := truncateSensitiveWordText(SensitiveWordScopeQuestion, big, sensitiveWordMaxTextBytes)
	if len(got) != sensitiveWordMaxTextBytes {
		t.Fatalf("262144 字节应截到预算 %d，实际 %d", sensitiveWordMaxTextBytes, len(got))
	}
	if !strings.Contains(captured.String(), "sensitive_word_text_truncated") {
		t.Fatalf("超预算必须打 WARN")
	}

	// 恰好等于预算：不截断、不打日志。
	captured.Reset()
	exact := strings.Repeat("a", sensitiveWordMaxTextBytes)
	got = truncateSensitiveWordText(SensitiveWordScopeQuestion, exact, sensitiveWordMaxTextBytes)
	if len(got) != sensitiveWordMaxTextBytes {
		t.Fatalf("恰好等于预算不得截断，实际 %d", len(got))
	}
	if captured.String() != "" {
		t.Fatalf("恰好等于预算不得打 WARN，实际：%q", captured.String())
	}

	// 3 字节中文：预算 240KiB 能被 3 整除（245760 / 3 = 81920），应恰好切齐。
	chinese := strings.Repeat("中", 100000) // 300000 字节
	got = truncateSensitiveWordText(SensitiveWordScopeQuestion, chinese, sensitiveWordMaxTextBytes)
	if len(got) != sensitiveWordMaxTextBytes {
		t.Fatalf("中文应恰好截到 %d 字节，实际 %d", sensitiveWordMaxTextBytes, len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("中文截断结果必须是合法 UTF-8")
	}
}

// TestCheckSensitiveWordScopeMarksTextTooLongDistinctly 钉住「超长」与「上游故障」可区分。
//
// 413 在 uapi 层同时满足 ErrTextTooLong 与 ErrUpstream（见 errors.go 的
// sentinelForStatus）。若 service 层不优先判 ErrTextTooLong，超长就会被写成
// 一条普通的「上游异常」，运维永远看不出是字节预算算错了。
//
// 用 mock detector，不消耗真实积分。
func TestCheckSensitiveWordScopeMarksTextTooLongDistinctly(t *testing.T) {
	tooLong := func(context.Context, string) (uapi.ProfanityCheckResponse, error) {
		// 模拟 uapi.CheckProfanity 在 413 时返回的错误形态（双哨兵）。
		return uapi.ProfanityCheckResponse{}, fmt.Errorf("%w: HTTP 413: Text exceeds maximum length of 256KB",
			errors.Join(uapi.ErrTextTooLong, uapi.ErrUpstream))
	}
	upstreamDown := func(context.Context, string) (uapi.ProfanityCheckResponse, error) {
		return uapi.ProfanityCheckResponse{}, fmt.Errorf("%w: HTTP 502: boom", uapi.ErrUpstream)
	}

	// db=nil / memo=nil：本条只验 413 的错误分类，缓存路径由缓存用例单独覆盖。
	result := checkSensitiveWordScope(context.Background(), nil, tooLong, SensitiveWordScopeQuestion, "题目", sensitiveWordQuestionMaxBytes, nil)
	if result.Status != "error" {
		t.Fatalf("超长应归为 error，实际 %q", result.Status)
	}
	if !strings.HasPrefix(result.Error, SensitiveWordErrorTextTooLong) {
		t.Fatalf("超长错误应以 %q 开头，实际 %q", SensitiveWordErrorTextTooLong, result.Error)
	}
	if !strings.Contains(result.Error, "256KB") {
		t.Fatalf("超长错误应保留上游原文，实际 %q", result.Error)
	}

	// 对照：普通上游故障不得带超长前缀（否则运维无法区分）。
	other := checkSensitiveWordScope(context.Background(), nil, upstreamDown, SensitiveWordScopeQuestion, "题目", sensitiveWordQuestionMaxBytes, nil)
	if other.Status != "error" {
		t.Fatalf("上游故障应归为 error，实际 %q", other.Status)
	}
	if strings.HasPrefix(other.Error, SensitiveWordErrorTextTooLong) {
		t.Fatalf("普通上游故障不得被标成超长，实际 %q", other.Error)
	}

	// 超长不阻断另一个对象，也不产生命中（旁路语义不变）。
	outcome := RunSensitiveWordCheck(context.Background(), tooLong, "题目", "答案")
	if outcome.Flag {
		t.Fatalf("超长失败不得判为命中")
	}
	if !strings.Contains(outcome.Error, SensitiveWordErrorTextTooLong) {
		t.Fatalf("折叠后的 Error 应保留超长标记，实际 %q", outcome.Error)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("两个对象都应留下结果，实际 %d", len(outcome.Results))
	}
}

func TestBuildSensitiveWordOutcomeSerializesGroupedScopes(t *testing.T) {
	outcome := BuildSensitiveWordOutcome([]SensitiveWordScopeResult{
		{Scope: SensitiveWordScopeQuestion, Status: "forbidden", Forbidden: true, Words: []string{"甲", "乙"}},
		{Scope: SensitiveWordScopeAnswer, Status: "clean", Words: []string{}},
	})

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(outcome.Words), &decoded); err != nil {
		t.Fatalf("Words 必须是合法 JSON：%v (%q)", err, outcome.Words)
	}
	if len(decoded) != 2 {
		t.Fatalf("必须保留两组结果，实际 %d", len(decoded))
	}
	if decoded[0]["scope"] != SensitiveWordScopeQuestion || decoded[0]["forbidden"] != true {
		t.Fatalf("题目组序列化错误：%+v", decoded[0])
	}
	if decoded[1]["scope"] != SensitiveWordScopeAnswer || decoded[1]["forbidden"] != false {
		t.Fatalf("答案组序列化错误：%+v", decoded[1])
	}
}

func TestNormalizeSensitiveWordListDedupesAndKeepsOrder(t *testing.T) {
	got := normalizeSensitiveWordList([]string{" 乙 ", "", "甲", "乙", "   "})
	want := []string{"乙", "甲"}
	if len(got) != len(want) {
		t.Fatalf("去重结果错误：%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("必须保序去重：got=%+v want=%+v", got, want)
		}
	}
	if normalized := normalizeSensitiveWordList(nil); normalized == nil || len(normalized) != 0 {
		t.Fatalf("nil 必须归一化成空切片：%+v", normalized)
	}
}

func TestGetSensitiveWordCheckEnabledWithDBDefaultsToFalse(t *testing.T) {
	t.Run("缺表", func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		if GetSensitiveWordCheckEnabledWithDB(db) {
			t.Fatalf("缺表时必须按关闭处理")
		}
	})
	t.Run("缺记录", func(t *testing.T) {
		db := newSensitiveWordTestDB(t)
		if GetSensitiveWordCheckEnabledWithDB(db) {
			t.Fatalf("缺记录时必须按关闭处理")
		}
	})
	t.Run("值非法", func(t *testing.T) {
		db := newSensitiveWordTestDB(t)
		if err := db.Create(&entity.SystemSetting{Key: SensitiveWordCheckEnabledSettingKey, Value: "not-a-bool"}).Error; err != nil {
			t.Fatalf("seed setting: %v", err)
		}
		if GetSensitiveWordCheckEnabledWithDB(db) {
			t.Fatalf("值非法时必须按关闭处理")
		}
	})
	t.Run("显式开启", func(t *testing.T) {
		db := newSensitiveWordTestDB(t)
		if err := db.Create(&entity.SystemSetting{Key: SensitiveWordCheckEnabledSettingKey, Value: "true"}).Error; err != nil {
			t.Fatalf("seed setting: %v", err)
		}
		if !GetSensitiveWordCheckEnabledWithDB(db) {
			t.Fatalf("显式 true 必须读成开启")
		}
	})
}

func TestIsSensitiveWordDetectorConfiguredWithDBRequiresSwitchAndKey(t *testing.T) {
	cases := []struct {
		name     string
		enabled  string
		apiKey   string
		expected bool
	}{
		{name: "都缺", enabled: "", apiKey: "", expected: false},
		{name: "只开开关", enabled: "true", apiKey: "", expected: false},
		{name: "只配密钥", enabled: "false", apiKey: "uapi-synthetic", expected: false},
		{name: "密钥格式非法", enabled: "true", apiKey: "plain-key", expected: false},
		{name: "都齐", enabled: "true", apiKey: "uapi-synthetic", expected: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newSensitiveWordTestDB(t)
			if tc.enabled != "" {
				if err := db.Create(&entity.SystemSetting{Key: SensitiveWordCheckEnabledSettingKey, Value: tc.enabled}).Error; err != nil {
					t.Fatalf("seed switch: %v", err)
				}
			}
			if tc.apiKey != "" {
				if err := db.Create(&entity.SystemSetting{Key: UAPIAPIKeySettingKey, Value: tc.apiKey}).Error; err != nil {
					t.Fatalf("seed key: %v", err)
				}
			}
			if got := IsSensitiveWordDetectorConfiguredWithDB(db); got != tc.expected {
				t.Fatalf("configured = %t，期望 %t", got, tc.expected)
			}
		})
	}
}

func TestBuildSensitiveWordDetectorWithDBReturnsNilWhenDisabled(t *testing.T) {
	db := newSensitiveWordTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: UAPIAPIKeySettingKey, Value: "uapi-synthetic"}).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if detector := BuildSensitiveWordDetectorWithDB(db); detector != nil {
		t.Fatalf("开关关闭时必须返回 nil（完全不外呼）")
	}
}

func TestResolveSensitiveWordSettingInputKeepsCurrentValueWhenOmitted(t *testing.T) {
	db := newSensitiveWordTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: SensitiveWordCheckEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	if got := ResolveSensitiveWordSettingInput(db, nil); !got {
		t.Fatalf("nil 表示未提交，必须保持库中现值 true")
	}
	disabled := false
	if got := ResolveSensitiveWordSettingInput(db, &disabled); got {
		t.Fatalf("显式 false 必须覆盖库中现值")
	}
}

func TestSaveSensitiveWordOutcomeWithDBWritesOnlySensitiveColumns(t *testing.T) {
	db := newSensitiveWordTestDB(t)
	ensureSensitiveLogTable(t, db)

	const answerText = "答案原文，一个字都不能改"
	if err := db.Exec(`INSERT INTO logs (id, module, action, description, old_answer, new_answer, create_time)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		77, "search", 3, "搜索完成 requestId=r-1", "请求原文", answerText, time.Now()).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}

	outcome := BuildSensitiveWordOutcome([]SensitiveWordScopeResult{
		{Scope: SensitiveWordScopeAnswer, Status: "forbidden", Forbidden: true, Words: []string{"敏感"}},
	})
	checkedAt := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	if err := SaveSensitiveWordOutcomeWithDB(db, 77, outcome, checkedAt); err != nil {
		t.Fatalf("save outcome: %v", err)
	}

	var row struct {
		Flag        bool
		Words       string
		CheckedAt   *time.Time
		CheckError  string
		NewAnswer   string
		OldAnswer   string
		Description string
	}
	if err := db.Raw(`SELECT sensitive_flag AS flag, sensitive_words AS words, sensitive_checked_at AS checked_at,
		sensitive_check_error AS check_error, new_answer AS new_answer, old_answer AS old_answer,
		description AS description FROM logs WHERE id = ?`, 77).Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !row.Flag {
		t.Fatalf("sensitive_flag 必须落库为 true")
	}
	if row.CheckedAt == nil {
		t.Fatalf("sensitive_checked_at 必须落库")
	}
	if row.NewAnswer != answerText {
		t.Fatalf("答案原文被改写：%q", row.NewAnswer)
	}
	if row.OldAnswer != "请求原文" || row.Description != "搜索完成 requestId=r-1" {
		t.Fatalf("原有列被改写：%+v", row)
	}
}

func TestSaveSensitiveWordOutcomeWithDBPersistsFalseFlag(t *testing.T) {
	// 「检测通过」必须写 false，而不能被 GORM 当成零值跳过（那样会与「未检测」混淆）。
	db := newSensitiveWordTestDB(t)
	ensureSensitiveLogTable(t, db)
	if err := db.Exec(`INSERT INTO logs (id, module, action, description, create_time) VALUES (?, ?, ?, ?, ?)`,
		88, "search", 3, "搜索完成", time.Now()).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}
	outcome := BuildSensitiveWordOutcome([]SensitiveWordScopeResult{
		{Scope: SensitiveWordScopeAnswer, Status: "clean", Words: []string{}},
	})
	if err := SaveSensitiveWordOutcomeWithDB(db, 88, outcome, time.Now()); err != nil {
		t.Fatalf("save outcome: %v", err)
	}

	var row struct {
		Flag      bool
		CheckedAt *time.Time
	}
	if err := db.Raw(`SELECT sensitive_flag AS flag, sensitive_checked_at AS checked_at FROM logs WHERE id = ?`, 88).Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Flag {
		t.Fatalf("未命中必须写 false")
	}
	if row.CheckedAt == nil {
		t.Fatalf("检测通过时也必须写检测时间，用于与「未检测」区分")
	}
}

func TestSaveSensitiveWordOutcomeWithDBRejectsMissingLogID(t *testing.T) {
	db := newSensitiveWordTestDB(t)
	ensureSensitiveLogTable(t, db)
	outcome := BuildSensitiveWordOutcome(nil)

	if err := SaveSensitiveWordOutcomeWithDB(db, 0, outcome, time.Now()); err == nil {
		t.Fatalf("缺少主键必须报错（不能按 requestId 模糊匹配）")
	}
	if err := SaveSensitiveWordOutcomeWithDB(db, 999, outcome, time.Now()); err == nil {
		t.Fatalf("日志不存在必须报错")
	}
	if err := SaveSensitiveWordOutcomeWithDB(nil, 1, outcome, time.Now()); err == nil {
		t.Fatalf("数据库不可用必须报错")
	}
}

// ───────────────────────── 缓存去重 ─────────────────────────
//
// 需求原文：「请同步修改数据库，避免反复检测，浪费API」。
// 下面每条测试都用**可数的检测器**断言「实际外呼次数」，而不是只看结果 ——
// 结果对但外呼了两次，正是这个功能要消灭的浪费（AGENTS.md 第 21 条）。

// countingDetector 返回检测器 + 计数读取函数。计数用指针而不是返回值快照，
// 因为检测器会被多次调用，快照会读到过期的 0。
func countingDetector(t *testing.T, response uapi.ProfanityCheckResponse, err error) (SensitiveWordDetector, func() int32) {
	t.Helper()
	var calls int32
	detector := func(context.Context, string) (uapi.ProfanityCheckResponse, error) {
		atomic.AddInt32(&calls, 1)
		if err != nil {
			return uapi.ProfanityCheckResponse{}, err
		}
		return response, nil
	}
	return detector, func() int32 { return atomic.LoadInt32(&calls) }
}

// newSensitiveWordCacheDB 建出带缓存表的测试库。
//
// 表结构由 entity 决定（与生产 AutoMigrate 一致），不手写 DDL：
// 列名/类型是持久化契约，测试手写一份就可能与迁移漂移。
func newSensitiveWordCacheDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newSensitiveWordTestDB(t)
	if err := db.AutoMigrate(&entity.SensitiveWordCache{}); err != nil {
		t.Fatalf("migrate sensitive word cache: %v", err)
	}
	return db
}

func countSensitiveWordCacheRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var rows int64
	if err := db.Model(&entity.SensitiveWordCache{}).Count(&rows).Error; err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	return rows
}

// 需求测试 1：同一文本第二次 → API 计数 == 1。
func TestSensitiveWordCacheDeduplicatesRepeatedText(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)

	first := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "重复的题目文本", "第一次的答案")
	if !first.Flag {
		t.Fatalf("首次检测应命中：%+v", first)
	}
	if got := calls(); got != 2 {
		t.Fatalf("首次检测题目与答案各外呼一次，实际 %d 次", got)
	}

	// 第二次：题目命中缓存；答案文本不同，仍然外呼一次。
	second := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "重复的题目文本", "第二次的答案")
	if got := calls(); got != 3 {
		t.Fatalf("第二次只该为**新**答案外呼一次，累计应为 3，实际 %d", got)
	}
	if !second.Results[0].Forbidden {
		t.Fatalf("缓存命中也必须保住命中结论：%+v", second.Results[0])
	}

	// 第三次：题目与答案都与库里已有的一致 → 零外呼。
	third := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "重复的题目文本", "第一次的答案")
	if got := calls(); got != 3 {
		t.Fatalf("全部命中缓存时不得再外呼，累计应仍为 3，实际 %d", got)
	}
	if !third.Flag {
		t.Fatalf("缓存结果必须完整还原命中：%+v", third)
	}
	if len(third.Results[0].Words) != 1 || third.Results[0].Words[0] != "敏感" {
		t.Fatalf("命中词必须从缓存还原：%+v", third.Results[0])
	}
	// 三次调用共涉及三段不同文本：题目 + 两条不同答案 → 3 行缓存。
	// （断言在这里写错很容易：题目被复用不会新增行，只有**新**答案才新增。）
	if got := countSensitiveWordCacheRows(t, db); got != 3 {
		t.Fatalf("题目 + 两条不同答案应共 3 行缓存，实际 %d", got)
	}
}

// 需求测试 2：题目与答案文本相同 → 只调一次。
func TestSensitiveWordCacheSendsIdenticalQuestionAndAnswerOnce(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, uapi.ProfanityCheckResponse{Status: "clean"}, nil)

	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "同一段文本", "同一段文本")

	if got := calls(); got != 1 {
		t.Fatalf("题目与答案相同必须只外呼一次，实际 %d 次", got)
	}
	if len(outcome.Results) != 2 {
		t.Fatalf("两个 scope 的结果都要保留（落库按 scope 分组），实际 %d", len(outcome.Results))
	}
	if got := countSensitiveWordCacheRows(t, db); got != 1 {
		t.Fatalf("同一文本只该有一行缓存，实际 %d", got)
	}
	// 跨次调用也必须只查一次库就够：第二次零外呼。
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, "同一段文本", "同一段文本")
	if got := calls(); got != 1 {
		t.Fatalf("第二次必须完全命中缓存，实际累计 %d 次", got)
	}
}

// 需求测试 3：Error != "" 且 AttemptCount < 3 → 重试。
func TestSensitiveWordCacheRetriesKnownFailureBelowLimit(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	if err := db.Create(&entity.SensitiveWordCache{
		TextHash:     hashSensitiveWordText("曾失败的文本"),
		Sensitive:    false,
		Words:        "[]",
		Error:        "uapi: HTTP 500",
		AttemptCount: sensitiveWordCacheMaxAttempts - 1,
	}).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)
	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "曾失败的文本", "")

	if got := calls(); got != 1 {
		t.Fatalf("未达上限必须重试（外呼一次），实际 %d 次", got)
	}
	if !outcome.Flag {
		t.Fatalf("重试成功后必须采用新结论：%+v", outcome)
	}

	var row entity.SensitiveWordCache
	if err := db.Where("text_hash = ?", hashSensitiveWordText("曾失败的文本")).Take(&row).Error; err != nil {
		t.Fatalf("load cache row: %v", err)
	}
	if strings.TrimSpace(row.Error) != "" {
		t.Fatalf("成功后必须清掉失败原因，否则下次仍会被当成失败：%q", row.Error)
	}
	if row.AttemptCount != 0 {
		t.Fatalf("成功后必须把失败计数归零，实际 %d", row.AttemptCount)
	}
	if !row.Sensitive {
		t.Fatalf("成功结论必须落库：%+v", row)
	}
}

// 需求测试 4：AttemptCount >= 3 → 不重试。
func TestSensitiveWordCacheStopsRetryingAtLimit(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	if err := db.Create(&entity.SensitiveWordCache{
		TextHash:     hashSensitiveWordText("反复失败的文本"),
		Sensitive:    false,
		Words:        "[]",
		Error:        "uapi: HTTP 503",
		AttemptCount: sensitiveWordCacheMaxAttempts,
	}).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)
	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "反复失败的文本", "")

	if got := calls(); got != 0 {
		t.Fatalf("达到上限后不得再外呼，实际 %d 次", got)
	}
	if outcome.Flag {
		t.Fatalf("已知失败不得判为命中：%+v", outcome)
	}
	if !strings.Contains(outcome.Error, "503") {
		t.Fatalf("必须保留上次失败原因供排障：%q", outcome.Error)
	}
	if outcome.Results[0].Status != "error" {
		t.Fatalf("达上限时应报告 error 状态，实际 %q", outcome.Results[0].Status)
	}
}

// 需求测试 5：表不存在 → fail-open，答题照常。
func TestSensitiveWordCacheFailsOpenWhenTableMissing(t *testing.T) {
	// 只建系统设置表，**不建** sensitive_word_cache。
	db := newSensitiveWordTestDB(t)
	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)

	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "题目文本", "答案文本")

	if got := calls(); got != 2 {
		t.Fatalf("缺表时必须照常外呼（fail-open），实际 %d 次", got)
	}
	if !outcome.Flag {
		t.Fatalf("缺表不得影响判定结果：%+v", outcome)
	}
	if strings.TrimSpace(outcome.Error) != "" {
		t.Fatalf("缺表是缓存不可用，不应记成检测失败：%q", outcome.Error)
	}
	// 确认确实没有建表（测试自己没偷偷建）。
	if db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
		t.Fatalf("这个用例的前提是缓存表不存在")
	}
}

// 需求测试 6：开关关闭 → 零外呼零写入。
//
// 开关判断在 controller 层（BuildSensitiveWordDetectorWithDB 返回 nil）。
// 这里断言两条独立的保证：① nil 检测器不外呼、不写缓存；
// ② 真实开关关闭时构建出的检测器就是 nil。
func TestSensitiveWordCacheZeroWorkWhenDisabled(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)

	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "", "")
	if got := calls(); got != 0 {
		t.Fatalf("空文本不得外呼，实际 %d 次", got)
	}
	for _, result := range outcome.Results {
		if result.Status != "skipped_empty" {
			t.Fatalf("空文本应为 skipped_empty：%+v", result)
		}
	}

	// nil 检测器：无检测通道，既不外呼也不写缓存。
	unconfigured := RunSensitiveWordCheckWithDB(context.Background(), db, nil, "题目", "答案")
	if !strings.Contains(unconfigured.Error, SensitiveWordScopeQuestion) {
		t.Fatalf("无检测通道必须报告未配置：%q", unconfigured.Error)
	}
	if got := countSensitiveWordCacheRows(t, db); got != 0 {
		t.Fatalf("无检测通道时不得写缓存，实际 %d 行", got)
	}

	// 真实开关关闭：BuildSensitiveWordDetectorWithDB 必须返回 nil（controller 据此零动作）。
	if err := db.Create(&entity.SystemSetting{Key: UAPIAPIKeySettingKey, Value: "uapi-synthetic"}).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if built := BuildSensitiveWordDetectorWithDB(db); built != nil {
		t.Fatalf("开关关闭时必须返回 nil，否则会出现外呼")
	}
}

// 缓存写入失败也必须照常返回结论（fail-open 的写侧）。
func TestSensitiveWordCacheWriteFailureKeepsOutcome(t *testing.T) {
	// 缓存表不存在 → Create 会失败，但结论必须完好。
	db := newSensitiveWordTestDB(t)
	detector, calls := countingDetector(t, forbiddenResponse("敏感"), nil)

	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "题目文本", "")
	if got := calls(); got != 1 {
		t.Fatalf("应照常外呼一次，实际 %d 次", got)
	}
	if !outcome.Flag || len(outcome.Results[0].Words) != 1 {
		t.Fatalf("缓存写失败不得损坏检测结论：%+v", outcome)
	}
}

// 失败也要落缓存（否则同一坏文本每次都会重试到底）。
func TestSensitiveWordCacheRecordsFailureWithAttemptCount(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, uapi.ProfanityCheckResponse{}, errors.New("uapi: HTTP 500"))

	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, "会失败的文本", "")
	if got := calls(); got != 1 {
		t.Fatalf("首次失败应外呼一次，实际 %d 次", got)
	}
	if outcome.Flag {
		t.Fatalf("失败不得判为命中：%+v", outcome)
	}

	var row entity.SensitiveWordCache
	if err := db.Where("text_hash = ?", hashSensitiveWordText("会失败的文本")).Take(&row).Error; err != nil {
		t.Fatalf("失败也必须写缓存行（供上限与重试判定）：%v", err)
	}
	if row.AttemptCount != 1 {
		t.Fatalf("失败后 AttemptCount 必须为 1，实际 %d", row.AttemptCount)
	}
	if !strings.Contains(row.Error, "500") {
		t.Fatalf("失败原因必须落库：%q", row.Error)
	}
	if row.Sensitive {
		t.Fatalf("失败不得被记成命中：%+v", row)
	}

	// 再失败一次 → 计数递增（连续失败累计）。
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, "会失败的文本", "")
	if err := db.Where("text_hash = ?", hashSensitiveWordText("会失败的文本")).Take(&row).Error; err != nil {
		t.Fatalf("reload cache row: %v", err)
	}
	if row.AttemptCount != 2 {
		t.Fatalf("第二次失败后应为 2，实际 %d", row.AttemptCount)
	}
	if got := calls(); got != 2 {
		t.Fatalf("两次调用各外呼一次，累计 2，实际 %d", got)
	}
}

// 缓存只在「送检文本」层面共享，跨 scope 通用：答案文本恰好等于另一题的题目文本时也能命中。
func TestSensitiveWordCacheIsKeyedByTextNotScope(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, uapi.ProfanityCheckResponse{Status: "clean"}, nil)

	RunSensitiveWordCheckWithDB(context.Background(), db, detector, "公共文本", "")
	if got := calls(); got != 1 {
		t.Fatalf("首次外呼一次，实际 %d", got)
	}
	// 同一文本这次出现在答案位 → 仍是同一段文本，同一结论，零外呼。
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, "别的题目", "公共文本")
	if got := calls(); got != 2 {
		t.Fatalf("只该为新题目外呼一次（公共文本命中缓存），累计应为 2，实际 %d", got)
	}
}

// 缓存键必须把「送检后」的文本作为输入：超长文本按**字节**预算截断后，同一前缀共享一行。
func TestSensitiveWordCacheHashesTruncatedSubmittedText(t *testing.T) {
	db := newSensitiveWordCacheDB(t)
	detector, calls := countingDetector(t, uapi.ProfanityCheckResponse{Status: "clean"}, nil)

	// 按字节预算算「多少个中文恰好填满」，不写死字符数：预算单位若变回 rune，
	// 这里的前缀长度就不再等于预算，下面的「只有一行缓存」会立刻不成立。
	chinese := "敏" // 3 字节
	prefix := strings.Repeat(chinese, sensitiveWordQuestionMaxBytes/len(chinese))
	if len(prefix) > sensitiveWordQuestionMaxBytes {
		t.Fatalf("前缀 %d 字节不该超过预算 %d", len(prefix), sensitiveWordQuestionMaxBytes)
	}
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, prefix+"尾部一", "")
	firstCalls := calls()
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, prefix+"尾部二", "")
	if got := calls(); got != firstCalls {
		t.Fatalf("截断后送检文本相同必须复用缓存，实际从 %d 增到 %d", firstCalls, got)
	}
	if got := countSensitiveWordCacheRows(t, db); got != 1 {
		t.Fatalf("截断后同前缀只该有一行缓存，实际 %d", got)
	}
}

// TestSensitiveWordCacheKeyUsesByteBudgetNotRuneBudget 是**防回退钉子**。
//
// 历史事故：上限曾用 rune 语义（先 2000 后 1000 字符），而上游真实限制是
// text 字段 262144 **字节**。rune 预算比真实上限小两个数量级，导致「题目被砍掉
// 大半、后半段敏感词全部漏检」且没有任何报错 —— 过度保守比直接报错更难被发现。
//
// 这条测试用**纯 ASCII**（1 字节 = 1 rune）与**中文**（3 字节 = 1 rune）两组文本，
// 断言送检长度由**字节预算**决定：
//   - 若有人把上限改回 rune 语义（即重新引入「按字符数」的常量），中文组送检字节数
//     会变成预算的 3 倍 → 超上游硬限制 → 本测试红；
//   - 若有人把预算调回 1000 这类小数，ASCII 组的送检字节数会远小于预算 → 本测试红。
//
// 已实测：把 truncateSensitiveWordText 的 `len(text)` 改成 utf8.RuneCountInString
// 后本测试立刻变红（中文送检 251760 字节 > 预算 245760），恢复后转绿。
func TestSensitiveWordCacheKeyUsesByteBudgetNotRuneBudget(t *testing.T) {
	db := newSensitiveWordCacheDB(t)

	// 记录每次真正送检的文本，直接断言**字节数**，而不是只数调用次数。
	var submitted []string
	detector := func(_ context.Context, text string) (uapi.ProfanityCheckResponse, error) {
		submitted = append(submitted, text)
		return uapi.ProfanityCheckResponse{Status: "clean"}, nil
	}

	// ① 纯 ASCII：超预算 → 必须截到**恰好** sensitiveWordMaxTextBytes 字节。
	asciiOver := strings.Repeat("a", sensitiveWordMaxTextBytes+4096)
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, asciiOver, "")
	if len(submitted) != 1 {
		t.Fatalf("应有 1 次外呼，实际 %d", len(submitted))
	}
	if got := len(submitted[0]); got != sensitiveWordMaxTextBytes {
		t.Fatalf("ASCII 超长应截到字节预算 %d，实际 %d 字节"+
			"（若上限被改回 rune 语义或改成小数值，这里就会不等）",
			sensitiveWordMaxTextBytes, got)
	}

	// ② 中文：字符数只有 ASCII 的 1/3，但**字节数**同样必须落在预算内。
	//    这一步是 rune→byte 的关键判别：rune 语义下会按 245760 个中文字符截断
	//    = 737280 字节，远超上游硬限制 262144，本断言立刻失败。
	chinese := "敏"
	chineseOver := strings.Repeat(chinese, sensitiveWordMaxTextBytes/len(chinese)+2000)
	submitted = nil
	RunSensitiveWordCheckWithDB(context.Background(), db, detector, chineseOver, "")
	if len(submitted) != 1 {
		t.Fatalf("应有 1 次外呼，实际 %d", len(submitted))
	}
	gotBytes := len(submitted[0])
	if gotBytes > sensitiveWordMaxTextBytes {
		t.Fatalf("中文送检 %d 字节超出字节预算 %d —— 上限疑似退回 rune 语义",
			gotBytes, sensitiveWordMaxTextBytes)
	}
	// 截断必须回退到完整 rune 边界，不能把某个汉字切成两半。
	if !utf8.ValidString(submitted[0]) {
		t.Fatalf("截断后文本必须是合法 UTF-8（回退到 rune 边界），实际被切断")
	}
	if gotBytes < sensitiveWordMaxTextBytes-len(chinese)+1 {
		t.Fatalf("中文送检只用了 %d 字节，远低于预算 %d —— 预算疑似被改小（如 1000）",
			gotBytes, sensitiveWordMaxTextBytes)
	}
	// 上游硬限制是 262144 字节，预算必须留余量。
	if gotBytes >= 262144 {
		t.Fatalf("送检 %d 字节贴着/超过上游硬限制 262144，没有安全余量", gotBytes)
	}
}

func TestHashSensitiveWordTextIsStableAndDomainSeparated(t *testing.T) {
	a := hashSensitiveWordText("文本")
	b := hashSensitiveWordText("文本")
	if a != b {
		t.Fatalf("同一文本必须得到同一 hash")
	}
	if len(a) != 64 {
		t.Fatalf("hash 必须是 64 位 hex（char(64) 主键），实际 %d: %q", len(a), a)
	}
	if a == hashSensitiveWordText("文本 ") {
		t.Fatalf("不同文本必须得到不同 hash")
	}
}
