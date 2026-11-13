package service

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// 题库「JSON 导入导出」与「CSV 导入导出」的合并契约（T-29）。
//
// 用户决策（2026-11-13）：两条路径都要活着，且**口径一致**。
// 本文件是那条「口径」的被测物：任何人改坏任一侧的键、上限、列集，这里必红。
//
// 三个被测面：
//
//	1. 去重键 —— (hash, course_name)，跨**格式**与跨**路径**都必须是同一把尺；
//	2. 行数上限 —— 题库导出 1 万 / 导入 5 千，且 CSV 与 JSON 受同一道闸；
//	3. 导出列集 —— 9 列与 entity.Tiku 字段逐一对应，hash 只导出不采信。

// questionImportBatch 是跨格式对照用的基准批次。
//
// 刻意把五类情况都放进来，因为它们**各自对应一种去重语义**，
// 少一类就会漏掉一类回归：
//
//	行1 全新题                         → created
//	行2 与行1 完全同键（本批内重复）    → skipped（本批内重复）
//	行3 与行1 同 hash、不同 course_name → created（**这是 (hash,course_name) 与 hash 的判别点**）
//	行4 不同题、同 course_name          → created（hash 变了就不算重复）
//	行5 options 不是合法 JSON           → failed（逐行失败不中断整批）
func questionImportBatch() (header []string, rows [][]string) {
	header = []string{"question", "type", "options", "answer", "course_name", "extra", "source_name"}
	rows = [][]string{
		{"同题同课", "0", `["A","B"]`, `["答案"]`, "数学", "", ""},
		{"同题同课", "0", `["A","B"]`, `["答案"]`, "数学", "", ""},
		{"同题同课", "0", `["A","B"]`, `["答案"]`, "物理", "", ""},
		{"另一道题", "0", `["A","B"]`, `["答案"]`, "数学", "", ""},
		{"坏行", "0", `not-json`, `["答案"]`, "数学", "", ""},
	}
	return header, rows
}

// toCSV 把表头 + 行编成 CSV 正文（标准引号转义）。
func questionBatchCSV(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	builder := &strings.Builder{}
	writer := csv.NewWriter(builder)
	if err := writer.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush csv: %v", err)
	}
	return []byte(builder.String())
}

// questionBatchJSON 把同一批数据编成**前端导出形态**（顶层数组，answer/options 是数组）。
//
// 这是 TikuComponent「导出选中」产出的形状（topicOverviewHelpers.mjs
// buildQuestionExportPayload），也就是「JSON 导入」要吃的形状。
func questionBatchJSON(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		fields := make([]string, 0, len(header))
		for i, column := range header {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			switch column {
			case "type", "question", "course_name", "extra", "source_name":
				// 字符串字段（type 允许数字也允许字符串，这里用字符串覆盖字符串分支）。
				fields = append(fields, fmt.Sprintf("%q:%q", column, value))
			default:
				// options / answer 是 JSON 数组**原文**，不能加引号。
				if strings.TrimSpace(value) == "" {
					value = "[]"
				}
				if strings.HasPrefix(strings.TrimSpace(value), "[") {
					fields = append(fields, fmt.Sprintf("%q:%s", column, value))
				} else {
					fields = append(fields, fmt.Sprintf("%q:%q", column, value))
				}
			}
		}
		items = append(items, "{"+strings.Join(fields, ",")+"}")
	}
	return []byte("[" + strings.Join(items, ",") + "]")
}

// questionRowResult 是「一行数据在某次导入里被判成了什么」。
//
// 刻意只取 result 而不是 index：明细的顺序在两套解析器里可能不同，
// 但**每一行被判成什么**必须一致，这才是口径。
type questionRowResult struct {
	row    int
	result string
	reason string
}

func questionRowResults(result ImportResult) []questionRowResult {
	out := make([]questionRowResult, 0, len(result.Details))
	for _, detail := range result.Details {
		out = append(out, questionRowResult{row: detail.Row, result: detail.Result, reason: detail.Reason})
	}
	return out
}

// ─────────────── 1. 跨格式契约：同一批数据，CSV 与 JSON 结果逐行相同 ───────────────

// 核心钉子：同一批题目分别走 CSV 与走 JSON，四种计数与逐行判定必须完全相同。
//
// 为什么这是「本项核心」：合并两条导入路径时最容易发生的事故是
// 「CSV 按 (hash,course_name) 去重、JSON 按 hash 去重」—— 两份结果是两个数字，
// 运营对着同一个文件换格式重导一次就会多出一批重复题，而且没人能解释为什么。
//
// 期望值全部写**字面量**（3/1/1 而不是从实现里取常量）：这条用例要在
// 「有人把去重键改成只用 hash」时变红，用常量拼期望值会让它跟着实现一起漂移。
func TestQuestionImportDedupeContractIsIdenticalAcrossCSVAndJSON(t *testing.T) {
	header, rows := questionImportBatch()

	type expectation struct {
		succeeded, skipped, failed int
		results                    map[int]string
	}

	// 第一轮：库为空。
	first := expectation{
		succeeded: 3,
		skipped:   1,
		failed:    1,
		results: map[int]string{
			2: ImportRowCreated, // 全新
			3: ImportRowSkipped, // 本批内重复（与第 2 行同键）
			4: ImportRowCreated, // 同 hash、**不同 course_name → 必须新建**
			5: ImportRowCreated, // 同 course_name、不同 hash → 必须新建
			6: ImportRowFailed,  // options 不是 JSON 数组
		},
	}
	// 第二轮：同样内容重导。除坏行外全部已存在（本批内重复仍按本批内重复判）。
	second := expectation{
		succeeded: 0,
		skipped:   4,
		failed:    1,
		results: map[int]string{
			2: ImportRowSkipped,
			3: ImportRowSkipped,
			4: ImportRowSkipped,
			5: ImportRowSkipped,
			6: ImportRowFailed,
		},
	}

	formats := []struct {
		name    string
		format  string
		content func(*testing.T) []byte
	}{
		{"csv", ImportFormatCSV, func(t *testing.T) []byte { return questionBatchCSV(t, header, rows) }},
		{"json", ImportFormatJSON, func(t *testing.T) []byte { return questionBatchJSON(t, header, rows) }},
	}

	for _, expectationRound := range []struct {
		label string
		want  expectation
	}{{"first", first}, {"second", second}} {
		// 每个格式各用一个独立库（同一次导入的对照必须是同一初始状态）。
		//
		// ⚠️ 必须用子测试包一层：newImportTestDB 的 DSN 由 t.Name() 拼成，
		// 同一个测试函数里连调两次拿到的是**同一个**内存库（cache=shared），
		// 第二个格式会看到第一个格式写进去的行，于是全被判成「已存在」。
		observed := make(map[string]ImportResult, len(formats))
		for _, candidate := range formats {
			candidate := candidate
			t.Run(expectationRound.label+"-"+candidate.name, func(t *testing.T) {
				db := newImportTestDB(t)
				content := candidate.content(t)
				// 第二轮前先把第一轮的结果落进同一个库。
				if expectationRound.label == "second" {
					if _, err := ImportDataWithDB(db, ImportDataOptions{
						Resource: ImportResourceQuestions,
						Format:   candidate.format,
						Content:  content,
					}); err != nil {
						t.Fatalf("%s warm-up import: %v", candidate.name, err)
					}
				}
				result, err := ImportDataWithDB(db, ImportDataOptions{
					Resource: ImportResourceQuestions,
					Format:   candidate.format,
					Content:  content,
				})
				if err != nil {
					t.Fatalf("%s import (%s): %v", candidate.name, expectationRound.label, err)
				}
				if result.Succeeded != expectationRound.want.succeeded ||
					result.Skipped != expectationRound.want.skipped ||
					result.Failed != expectationRound.want.failed {
					t.Fatalf("%s %s round: succeeded/skipped/failed = %d/%d/%d, want %d/%d/%d (details=%+v)",
						candidate.name, expectationRound.label,
						result.Succeeded, result.Skipped, result.Failed,
						expectationRound.want.succeeded, expectationRound.want.skipped, expectationRound.want.failed,
						result.Details)
				}
				assertQuestionRowResults(t, candidate.name+" "+expectationRound.label, result, expectationRound.want.results)
			})
		}
		// 跨格式相等：把两个子测试的结果各算一遍期望（子测试的库已隔离，
		// 这里在**同一个测试作用域**重新断言两者的计数与逐行判定完全一致）。
		for _, candidate := range formats {
			db := newImportTestDBForName(t, expectationRound.label+"-"+candidate.name)
			content := candidate.content(t)
			if expectationRound.label == "second" {
				if _, err := ImportDataWithDB(db, ImportDataOptions{
					Resource: ImportResourceQuestions, Format: candidate.format, Content: content,
				}); err != nil {
					t.Fatalf("cross-format warm-up %s: %v", candidate.name, err)
				}
			}
			result, err := ImportDataWithDB(db, ImportDataOptions{
				Resource: ImportResourceQuestions, Format: candidate.format, Content: content,
			})
			if err != nil {
				t.Fatalf("cross-format %s: %v", candidate.name, err)
			}
			observed[candidate.name] = result
		}
		csvResult := observed["csv"]
		jsonResult := observed["json"]
		if csvResult.Succeeded != jsonResult.Succeeded ||
			csvResult.Skipped != jsonResult.Skipped ||
			csvResult.Failed != jsonResult.Failed ||
			csvResult.Total != jsonResult.Total {
			t.Fatalf("%s round: CSV and JSON disagree: csv=%d/%d/%d total=%d json=%d/%d/%d total=%d",
				expectationRound.label,
				csvResult.Succeeded, csvResult.Skipped, csvResult.Failed, csvResult.Total,
				jsonResult.Succeeded, jsonResult.Skipped, jsonResult.Failed, jsonResult.Total)
		}
		if got, want := questionRowResults(jsonResult), questionRowResults(csvResult); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s round: per-row verdicts differ\n csv=%+v\njson=%+v", expectationRound.label, want, got)
		}
	}
}

func assertQuestionRowResults(t *testing.T, label string, result ImportResult, want map[int]string) {
	t.Helper()
	if len(result.Details) != len(want) {
		t.Fatalf("%s: detail rows = %d, want %d (%+v)", label, len(result.Details), len(want), result.Details)
	}
	seen := make(map[int]bool, len(want))
	for _, detail := range result.Details {
		expected, ok := want[detail.Row]
		if !ok {
			t.Fatalf("%s: unexpected row %d in details (%+v)", label, detail.Row, detail)
		}
		seen[detail.Row] = true
		if detail.Result != expected {
			t.Fatalf("%s: row %d result = %q, want %q (reason=%q)", label, detail.Row, detail.Result, expected, detail.Reason)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("%s: only saw rows %v, want %d distinct rows", label, seen, len(want))
	}
}

// ─────────────── 2. 去重键本身：(hash, course_name)，NUL 分隔 ───────────────

// 键必须同时含 hash 与 course_name，且拼接不可产生歧义。
//
// 「只用 hash」这一变异会让：
//   - 同一题挂两个课程的第二条被当成重复（导入静默丢题）；
//   - 与 DB 唯一索引 hash_course_idx 的口径分叉。
func TestQuestionImportDedupeKeyCarriesHashAndCourseName(t *testing.T) {
	const hashA = "0123456789abcdef0123456789abcdef"
	sameCourse := QuestionImportDedupeKey(hashA, "数学")
	otherCourse := QuestionImportDedupeKey(hashA, "物理")
	if sameCourse == otherCourse {
		t.Fatalf("same hash with different course names must produce different keys: %q", sameCourse)
	}
	if !strings.Contains(sameCourse, hashA) {
		t.Fatalf("key %q must contain the hash", sameCourse)
	}
	if !strings.Contains(sameCourse, "数学") {
		t.Fatalf("key %q must contain the course name", sameCourse)
	}
	// 拼接歧义防线："a" + "b|c" 与 "a|b" + "c" 用可打印分隔符会撞键；NUL 不会。
	if QuestionImportDedupeKey("ab", "c") == QuestionImportDedupeKey("a", "bc") {
		t.Fatal("key concatenation is ambiguous: different (hash, course) pairs collided")
	}
	if QuestionImportDedupeKey(hashA, "") != QuestionImportDedupeKey(hashA, "") {
		t.Fatal("key must be deterministic")
	}
	if QuestionImportDedupeKey(hashA, "") == QuestionImportDedupeKey(hashA, "数学") {
		t.Fatal("empty course name must not collide with a real course name")
	}
}

// CSV 的 hash 列是**导出核对列**，导入时一律重算，不采信文件里的值。
//
// 采信文件值会让「手改过的 CSV」直接决定唯一索引的取值 —— 既能绕过重复检测，
// 也能让两条不同题目落到同一 hash 上。这条用例把「重算」钉死。
func TestQuestionImportIgnoresHashColumnFromCSV(t *testing.T) {
	db := newImportTestDB(t)
	// 先落一条真行（hash 由 BuildTikuHash 算）。
	seedQuestionRow(t, db, "同题同课", []string{"A", "B"}, 0, "数学", false)

	// 同一道题，但 CSV 的 hash 列填成"看起来不同"的假值。
	forged := util.BuildTikuHash("完全不同的题", []string{"Z"}, 7)
	content := []byte("id,question,type,options,answer,course_name,extra,source_name,hash\n" +
		`9,同题同课,0,"[""A"",""B""]","[""答案""]",数学,,,` + forged + "\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatCSV,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Skipped != 1 || result.Succeeded != 0 {
		t.Fatalf("forged hash column must not defeat dedupe: got succeeded=%d skipped=%d details=%+v",
			result.Succeeded, result.Skipped, result.Details)
	}
	var count int64
	if err := db.Model(&entity.Tiku{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("tiku rows = %d, want 1 (no row may be created from the forged row)", count)
	}
}

// newImportTestDBForName 开一个**名字由调用方指定**的独立内存库。
//
// newImportTestDB 的 DSN 取自 t.Name()，在同一测试作用域里连调两次会拿到
// 同一个 `cache=shared` 内存库。跨格式对照必须两边从同一初始状态出发，
// 所以这里显式给不同的库名。
func newImportTestDBForName(t *testing.T, name string) *gorm.DB {
	t.Helper()
	safe := strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(name)
	dsn := fmt.Sprintf("file:question-contract-%s?mode=memory&cache=shared", safe)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	for _, model := range []any{&entity.SystemSetting{}, &entity.AsyncTask{}, &entity.User{}, &entity.Tiku{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T (%s): %v", model, dsn, err)
		}
	}
	return db
}

// seedQuestionRow 直接落一条题库行（hash 走生产同一个 util.BuildTikuHash）。
//
// archived=true 时把 deleted_at 写成非 NULL：归档题必须被导出/导入两侧都视为「不存在」。
func seedQuestionRow(t *testing.T, db *gorm.DB, question string, options []string, questionType int, courseName string, archived bool) entity.Tiku {
	t.Helper()
	encodedOptions, err := json.Marshal(options)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	row := entity.Tiku{
		Question:   question,
		Type:       int32(questionType),
		Options:    string(encodedOptions),
		Answer:     `["答案"]`,
		Hash:       util.BuildTikuHash(question, options, questionType),
		CourseName: courseName,
		SourceName: "人工导入",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create tiku: %v", err)
	}
	if archived {
		if err := db.Exec("UPDATE tiku SET deleted_at = ? WHERE id = ?", time.Now(), row.ID).Error; err != nil {
			t.Fatalf("archive tiku: %v", err)
		}
	}
	return row
}

// ─────────────── 3. 行数上限：题库导出 1 万 / 导入 5 千，且两格式同闸 ───────────────

// 上限的字面值与倍数关系。期望值写字面量是为了让「有人把 1 万改成 10 万」时必红。
func TestQuestionImportExportLimitsAreConsistent(t *testing.T) {
	if QuestionExportMaxRows != 10000 {
		t.Fatalf("QuestionExportMaxRows = %d, want 10000 (题库异步导出单任务上限)", QuestionExportMaxRows)
	}
	if QuestionImportMaxRows != 5000 {
		t.Fatalf("QuestionImportMaxRows = %d, want 5000 (题库单批导入上限)", QuestionImportMaxRows)
	}
	if QuestionExportMaxRows != 2*QuestionImportMaxRows {
		t.Fatalf("export/import limits must be a fixed 2x relation, got %d vs %d",
			QuestionExportMaxRows, QuestionImportMaxRows)
	}
	if got := ExportRowLimitOf(ExportResourceQuestions); got != 10000 {
		t.Fatalf("ExportRowLimitOf(questions) = %d, want 10000", got)
	}
	if got := ExportRowLimitOf(ExportResourceUsers); got != 100_000 {
		t.Fatalf("ExportRowLimitOf(users) = %d, want 100000 (通用上限不得被题库上限带偏)", got)
	}
	if got := ImportRowLimitOf(ImportResourceQuestions); got != 5000 {
		t.Fatalf("ImportRowLimitOf(questions) = %d, want 5000", got)
	}
	if got := ImportRowLimitOf(ImportResourceBlacklist); got != 5000 {
		t.Fatalf("ImportRowLimitOf(blacklist) = %d, want 5000", got)
	}
}

// 显式 Limit 不许突破资源上限（否则「传 10 万」就等于上限不存在）。
func TestQuestionExportLimitCannotBeRaisedByCaller(t *testing.T) {
	cases := []struct {
		name      string
		requested int
		want      int
	}{
		{"default", 0, 10000},
		{"unlimited-negative", -1, 10000},
		{"over-ceiling", 100_000, 10000},
		{"exactly-ceiling", 10000, 10000},
		{"below-ceiling", 10, 10},
	}
	for _, testCase := range cases {
		if got := exportResolveLimit(ExportResourceQuestions, testCase.requested); got != testCase.want {
			t.Fatalf("%s: exportResolveLimit(questions, %d) = %d, want %d",
				testCase.name, testCase.requested, got, testCase.want)
		}
	}
	// 通用资源仍按通用上限。
	if got := exportResolveLimit(ExportResourceUsers, -1); got != 100_000 {
		t.Fatalf("exportResolveLimit(users, -1) = %d, want 100000", got)
	}
}

// 「CSV / JSON 受同一上限」—— 5001 行的题目，两种格式都必须被同一道闸拦下。
//
// 这条是用户提到的「否则会 JSON 能导 5 万条、CSV 只能 1 万条」的直接钉子。
func TestQuestionImportRowLimitAppliesToBothFormats(t *testing.T) {
	header := []string{"question", "type", "options", "answer", "course_name"}
	rows := make([][]string, 0, 5001)
	for i := 0; i < 5001; i++ {
		rows = append(rows, []string{
			fmt.Sprintf("批量题-%d", i), "0", `["A"]`, `["答案"]`, "数学",
		})
	}
	formats := []struct {
		name    string
		format  string
		content []byte
	}{
		{"csv", ImportFormatCSV, questionBatchCSV(t, header, rows)},
		{"json", ImportFormatJSON, questionBatchJSON(t, header, rows)},
	}
	for _, candidate := range formats {
		db := newImportTestDB(t)
		_, err := ImportDataWithDB(db, ImportDataOptions{
			Resource: ImportResourceQuestions,
			Format:   candidate.format,
			Content:  candidate.content,
		})
		if err == nil {
			t.Fatalf("%s: 5001 questions must be rejected", candidate.name)
		}
		if !strings.Contains(err.Error(), "5000") {
			t.Fatalf("%s: error should name the 5000 limit, got %v", candidate.name, err)
		}
		// 超限是**整体性错误**：一行都不能写。
		var count int64
		if countErr := db.Model(&entity.Tiku{}).Count(&count).Error; countErr != nil {
			t.Fatalf("%s count: %v", candidate.name, countErr)
		}
		if count != 0 {
			t.Fatalf("%s: rejected import must write nothing, got %d rows", candidate.name, count)
		}
	}
}

// 恰好 5000 行必须能过（边界不许是「>=」）。
func TestQuestionImportAcceptsExactlyImportLimit(t *testing.T) {
	header := []string{"question", "type", "options", "answer", "course_name"}
	rows := make([][]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		rows = append(rows, []string{fmt.Sprintf("边界题-%d", i), "0", `["A"]`, `["答案"]`, "数学"})
	}
	db := newImportTestDB(t)
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatJSON,
		Content:  questionBatchJSON(t, header, rows),
	})
	if err != nil {
		t.Fatalf("exactly 5000 rows must be accepted: %v", err)
	}
	if result.Succeeded != 5000 {
		t.Fatalf("succeeded = %d, want 5000", result.Succeeded)
	}
}

// ─────────────── 4. 导出列与 entity.Tiku 字段对齐 ───────────────

// 9 个导出列必须逐一对应 entity.Tiku 的真实列，且唯一被排除的是 deleted_at。
//
// 期望值写字面量（而不是从 exportResourceSpecs 取）：
// 这条用例要防的正是「有人给 questions 加了半列 / 删了一列」。
func TestQuestionExportColumnsMatchTikuEntity(t *testing.T) {
	want := []string{"id", "question", "type", "options", "answer", "course_name", "extra", "source_name", "hash"}
	got, err := ExportColumnsOf(ExportResourceQuestions)
	if err != nil {
		t.Fatalf("ExportColumnsOf(questions): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}

	entityColumns := tikuEntityColumns(t)
	for _, column := range got {
		if !entityColumns[column] {
			t.Fatalf("export column %q is not a column of entity.Tiku (entity columns: %v)", column, questionContractKeysOf(entityColumns))
		}
	}
	// 唯一缺席的实体列必须是归档标记。
	missing := make([]string, 0, 1)
	for column := range entityColumns {
		if !questionContractContains(got, column) {
			missing = append(missing, column)
		}
	}
	if !reflect.DeepEqual(missing, []string{"deleted_at"}) {
		t.Fatalf("only deleted_at may be excluded from the question export, missing = %v", missing)
	}
	// 归档题不属于「存在」的题，绝不能出现在导出里 —— 这里顺带钉住行为侧。
	db := newImportTestDB(t)
	seedQuestionRow(t, db, "在库题", []string{"A"}, 0, "数学", false)
	seedQuestionRow(t, db, "已归档题", []string{"A"}, 0, "数学", true)
	result, err := ExportCSVWithDB(db, ExportDataOptions{Resource: ExportResourceQuestions})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if result.Rows != 1 {
		t.Fatalf("exported rows = %d, want 1 (archived rows must be excluded)", result.Rows)
	}
	if strings.Contains(string(result.Content), "已归档题") {
		t.Fatalf("archived question leaked into export:\n%s", string(result.Content))
	}
}

// tikuEntityColumns 返回 entity.Tiku 在数据库里的真实列名集合。
//
// 用 gorm 的 schema 解析而不是自己拆 `gorm:"column:..."` 标签：
// **ID 没有 column 标签**（gorm 用 NamingStrategy 把它映射成 `id`），
// 只读标签会漏掉主键，进而误判「导出列 id 不是实体列」。
func tikuEntityColumns(t *testing.T) map[string]bool {
	t.Helper()
	parsed, err := schema.Parse(&entity.Tiku{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse entity.Tiku schema: %v", err)
	}
	columns := make(map[string]bool, len(parsed.DBNames))
	for _, name := range parsed.DBNames {
		columns[name] = true
	}
	if len(columns) == 0 {
		t.Fatal("could not read column names from entity.Tiku")
	}
	if !columns["id"] || !columns["deleted_at"] {
		t.Fatalf("schema parse missed expected columns, got %v", questionContractKeysOf(columns))
	}
	return columns
}

// questionContractKeysOf 把集合转成切片（仅本文件的失败信息用）。
func questionContractKeysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

// questionContractContains 判断切片是否含目标项。
func questionContractContains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
