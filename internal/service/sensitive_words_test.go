package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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

	result := checkSensitiveWordScope(context.Background(), tooLong, SensitiveWordScopeQuestion, "题目", sensitiveWordQuestionMaxBytes)
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
	other := checkSensitiveWordScope(context.Background(), upstreamDown, SensitiveWordScopeQuestion, "题目", sensitiveWordQuestionMaxBytes)
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
