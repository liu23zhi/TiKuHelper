package service

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// newImportTestDB 建一个隔离的导入测试库（含黑名单两张表与题库表）。
//
// AutoMigrate 逐个调用（不是一次传两个）：一次传多个模型时 GORM 会合并建表语句，
// 物理表可能只剩几列 —— captain 广播的已验证事故。
func newImportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAsyncTaskTestDB(t)
	for _, model := range []any{&entity.BlacklistEntry{}, &entity.BlacklistHit{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	return db
}

// xlsxBytes 生成一个最小 xlsx（第一行表头 + 若干数据行）。
func xlsxBytes(t *testing.T, rows [][]string) []byte {
	t.Helper()
	file := excelize.NewFile()
	sheet := file.GetSheetName(0)
	for rowIndex, row := range rows {
		for columnIndex, cell := range row {
			axis, err := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+1)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}
			if err := file.SetCellValue(sheet, axis, cell); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	buffer := &bytes.Buffer{}
	if err := file.Write(buffer); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	_ = file.Close()
	return buffer.Bytes()
}

// ─────────────────────────── xls：必写测试（任务书） ───────────────────────────

// xls 导入失败的错误信息必须含「另存为 xlsx」（任务书必写测试）。
//
// 断言两件事，缺一条这个用例就是空转：
//  1. 真 BIFF（OLE2 复合文档）→ 返回错误（不静默返回空表）；
//  2. 错误文案**必须**含「另存为 xlsx」——那是用户唯一能立刻执行的动作。
func TestImportDataXlsFailureMentionsSaveAsXlsx(t *testing.T) {
	// 最小 OLE2 头：只要前 8 字节是 D0CF11E0A1B11AE1，就会被判成真 BIFF。
	ole2 := append([]byte{}, ole2Magic...)
	ole2 = append(ole2, bytes.Repeat([]byte{0x00}, 504)...)

	_, _, err := ParseImportRows(ImportFormatXls, ole2)
	if err == nil {
		t.Fatal("real BIFF (.xls) must not be parsed silently")
	}
	if !errors.Is(err, ErrImportXlsUnsupported) {
		t.Fatalf("err = %v, want ErrImportXlsUnsupported", err)
	}
	if !strings.Contains(err.Error(), "另存为 xlsx") {
		t.Fatalf("error message must contain 另存为 xlsx, got: %q", err.Error())
	}

	// 从 ImportDataWithDB 走一遍（端到端文案同样要含「另存为 xlsx」）：
	db := newImportTestDB(t)
	_, err = ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatXls,
		Filename: "list.xls",
		Content:  ole2,
	})
	if err == nil {
		t.Fatal("ImportDataWithDB must refuse real BIFF")
	}
	if !strings.Contains(err.Error(), "另存为 xlsx") {
		t.Fatalf("ImportDataWithDB error must contain 另存为 xlsx, got: %q", err.Error())
	}
}

// 「改名成 .xls 的 xlsx」必须真的解析成功（这层「尽力」不是空话）。
func TestImportDataXlsWithXlsxContentParses(t *testing.T) {
	db := newImportTestDB(t)
	content := xlsxBytes(t, [][]string{
		{"value", "reason"},
		{"13800000001", "恶意刷单"},
	})
	// 扩展名给 .xls，内容其实是 ZIP → 必须走通。
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatXls,
		Filename: "renamed.xls",
		Content:  content,
	})
	if err != nil {
		t.Fatalf("xlsx content under .xls must parse: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 1 succeeded 0 failed", result)
	}
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("blacklist entries = %d, want 1", count)
	}
}

// 「Excel 另存的单一文件网页」伪 .xls 也必须解析成功。
func TestImportDataXlsWithHTMLContentParses(t *testing.T) {
	html := []byte(`<html><body><table>
<tr><td>value</td><td>reason</td></tr>
<tr><td>13800000002</td><td>测试 &amp; 验证</td></tr>
</table></body></html>`)
	result, err := ImportDataWithDB(newImportTestDB(t), ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatXls,
		Filename: "webpage.xls",
		Content:  html,
	})
	if err != nil {
		t.Fatalf("HTML .xls must parse: %v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("result = %+v, want 1 succeeded", result)
	}
	// 表头行不能被当成数据行（这是 HTML 分支最典型的错法）：
	// total 必须是 1（只有一行数据），且 Key 是数据行而不是 "value"。
	if result.Total != 1 {
		t.Fatalf("total = %d, want 1 (header must not be counted as data)", result.Total)
	}
	if result.Details[0].Key != "13800000002" {
		t.Fatalf("detail key = %q, want 13800000002", result.Details[0].Key)
	}
}

// ─────────────────────────── 格式识别 ───────────────────────────

// 扩展名优先、内容兜底：改名成 .csv 的 xlsx 必须按 xlsx 解析（否则会报一堆字段数错误）。
func TestImportDataDetectFormatPrefersContent(t *testing.T) {
	content := xlsxBytes(t, [][]string{{"value"}, {"1"}})
	// 扩展名写 .csv，内容其实是 xlsx：格式识别按扩展名 → 但解析会失败；
	// 因此这里断言的是「内容可识别为 xlsx」这一兜底能力。
	detected, err := DetectImportFormat("no-extension", content)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if detected != ImportFormatXlsx {
		t.Fatalf("detected = %q, want xlsx (content magic)", detected)
	}
	// JSON 内容
	if detected, err := DetectImportFormat("data", []byte(`[{"question":"q"}]`)); err != nil || detected != ImportFormatJSON {
		t.Fatalf("json detect = %q err=%v", detected, err)
	}
	// 完全无法识别
	if _, err := DetectImportFormat("data", []byte("\x00\x01\x02")); !errors.Is(err, ErrImportFormatUnsupported) {
		t.Fatalf("unrecognized content err = %v, want ErrImportFormatUnsupported", err)
	}
	// 四个扩展名都要认
	for name, want := range map[string]string{
		"a.csv": ImportFormatCSV, "a.json": ImportFormatJSON,
		"a.xlsx": ImportFormatXlsx, "a.xls": ImportFormatXls, "A.CSV": ImportFormatCSV,
	} {
		if got, err := DetectImportFormat(name, nil); err != nil || got != want {
			t.Fatalf("DetectImportFormat(%q) = %q err=%v, want %q", name, got, err, want)
		}
	}
}

// ─────────────────────────── 黑名单导入 ───────────────────────────

// 部分成功语义 + 三种归类的跳过原因必须可区分（107 Q13b / Q13c）。
func TestImportDataBlacklistPartialSuccess(t *testing.T) {
	db := newImportTestDB(t)
	// 先放一条已存在的（用于验证「已存在」这种跳过）。
	first := entity.BlacklistEntry{
		Kind: BlacklistKindPhone, Pattern: "13800000009",
		ValueHash: BlacklistPatternHashHex("13800000009"), MatchType: BlacklistMatchExact,
		Reason: "预置", Enabled: true,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	content := []byte("\uFEFFvalue,reason,expires_at\r\n" +
		"13800000001,恶意刷单,2026-12-31\r\n" +
		"13800000002,恶意刷单,\r\n" +
		"13800000001,重复行,2026-12-31\r\n" +
		"abc,格式错误,\r\n" +
		"13800000009,已存在,\r\n")

	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatCSV,
		Filename: "blacklist.csv",
		Content:  content,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Total != 5 {
		t.Fatalf("total = %d, want 5", result.Total)
	}
	if result.Succeeded != 2 || result.Skipped != 2 || result.Failed != 1 {
		t.Fatalf("result = succeeded %d / skipped %d / failed %d, want 2/2/1 (details=%+v)",
			result.Succeeded, result.Skipped, result.Failed, result.Details)
	}
	// 三种原因必须可区分（糊成一个数字运营没法判断该不该重跑）。
	var duplicatedReason, existedReason, invalidReason string
	for _, detail := range result.Details {
		switch {
		case detail.Row == 4:
			duplicatedReason = detail.Reason
		case detail.Row == 6:
			existedReason = detail.Reason
		case detail.Row == 5:
			invalidReason = detail.Reason
		}
	}
	if !strings.Contains(duplicatedReason, "本批内重复") {
		t.Fatalf("row 4 reason = %q, want 本批内重复", duplicatedReason)
	}
	if !strings.Contains(existedReason, "已存在") {
		t.Fatalf("row 6 reason = %q, want 已存在", existedReason)
	}
	if invalidReason == "" {
		t.Fatal("row 5 (invalid value) must have a failure reason")
	}
	// 错误报告只包含问题行（2 skipped + 1 failed = 3 行数据 + 表头）。
	if len(result.ErrorReport) == 0 {
		t.Fatal("expected an error report")
	}
	reportLines := strings.Split(strings.TrimSpace(string(result.ErrorReport)), "\n")
	if len(reportLines) != 4 {
		t.Fatalf("error report lines = %d, want 4 (header + 3 problem rows):\n%s",
			len(reportLines), string(result.ErrorReport))
	}
	// 落库：2 条新 + 1 条预置。
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("blacklist entries = %d, want 3", count)
	}
}

// 表头缺必填列 → 整体 400 类错误，且文案点名缺了哪一列。
func TestImportDataRequiresHeaderColumns(t *testing.T) {
	_, err := ImportDataWithDB(newImportTestDB(t), ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatCSV,
		Content:  []byte("kind,reason\nip,测试\n"),
	})
	if !errors.Is(err, ErrImportHeaderInvalid) {
		t.Fatalf("err = %v, want ErrImportHeaderInvalid", err)
	}
	if !strings.Contains(err.Error(), "value") {
		t.Fatalf("err must name the missing column: %q", err.Error())
	}
}

// 行数上限（107 Q13d：5000）必须真的拦住。
func TestImportDataRejectsOverLimit(t *testing.T) {
	builder := strings.Builder{}
	builder.WriteString("value\n")
	for i := 0; i < 6; i++ {
		builder.WriteString("1380000000")
		builder.WriteString(string(rune('0' + i)))
		builder.WriteString("\n")
	}
	_, err := ImportDataWithDB(newImportTestDB(t), ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatCSV,
		Content:  []byte(builder.String()),
		Limit:    5,
	})
	if !errors.Is(err, ErrImportRowLimitExceeded) {
		t.Fatalf("err = %v, want ErrImportRowLimitExceeded", err)
	}
}

// ─────────────────────────── 用户导入：弱密码二次确认（任务书 ④） ───────────────────────────

// 弱密码必须先拒绝并要求确认，确认后才允许导入；且**拒绝时一行都不能写**。
func TestImportDataUsersWeakPasswordRequiresConfirmation(t *testing.T) {
	db := newImportTestDB(t)
	content := []byte("username,password\nweakuser,12345678\nstronguser,Abc12345!\n")

	// 第一次：未确认 → 返回错误 + 具体行号，且库里零用户。
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceUsers,
		Format:   ImportFormatCSV,
		Content:  content,
	})
	if !errors.Is(err, ErrImportWeakPasswordConfirmRequired) {
		t.Fatalf("err = %v, want ErrImportWeakPasswordConfirmRequired", err)
	}
	var count int64
	if err := db.Model(&entity.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("no user may be written before confirmation, got %d", count)
	}
	if len(result.WeakPasswordRows) != 1 || result.WeakPasswordRows[0] != 2 {
		t.Fatalf("weakPasswordRows = %v, want [2]", result.WeakPasswordRows)
	}
	if !strings.Contains(err.Error(), "另") && !strings.Contains(err.Error(), "确认") {
		t.Fatalf("error message must ask for confirmation: %q", err.Error())
	}

	// 第二次：确认 → 两行都导入成功（弱密码不是拒绝）。
	result, err = ImportDataWithDB(db, ImportDataOptions{
		Resource:            ImportResourceUsers,
		Format:              ImportFormatCSV,
		Content:             content,
		ConfirmWeakPassword: true,
	})
	if err != nil {
		t.Fatalf("confirmed import: %v", err)
	}
	if result.Succeeded != 2 {
		t.Fatalf("succeeded = %d, want 2 (details=%+v)", result.Succeeded, result.Details)
	}
	var users []entity.User
	if err := db.Order("id asc").Find(&users).Error; err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %d, want 2", len(users))
	}
	// 密码必须**哈希后**落库（明文入库等于泄漏）。
	for _, user := range users {
		if user.Password == "12345678" || user.Password == "Abc12345!" {
			t.Fatalf("password must be hashed, got plaintext %q", user.Password)
		}
		if !strings.HasPrefix(user.Password, "$2") {
			t.Fatalf("password = %q, want bcrypt hash", user.Password)
		}
	}
}

// 用户名已存在 → skipped；本批内重复 → skipped 且原因不同。
func TestImportDataUsersSkipsExistingAndDuplicates(t *testing.T) {
	db := newImportTestDB(t)
	if err := db.Create(&entity.User{Username: "taken", Password: "x", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	content := []byte("username,password\ntaken,Abc12345!\nfresh,Abc12345!\nFresh,Abc12345!\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceUsers,
		Format:   ImportFormatCSV,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Succeeded != 1 || result.Skipped != 2 {
		t.Fatalf("result = %d succeeded / %d skipped, want 1/2 (details=%+v)",
			result.Succeeded, result.Skipped, result.Details)
	}
	var existingReason, duplicateReason string
	for _, detail := range result.Details {
		if detail.Row == 2 {
			existingReason = detail.Reason
		}
		if detail.Row == 4 {
			duplicateReason = detail.Reason
		}
	}
	if !strings.Contains(existingReason, "已存在") {
		t.Fatalf("row 2 reason = %q, want 已存在", existingReason)
	}
	if !strings.Contains(duplicateReason, "本批内重复") {
		t.Fatalf("row 4 reason = %q, want 本批内重复", duplicateReason)
	}
}

// 手机号非法 → failed（不是 skipped），且不影响同批其它行。
func TestImportDataUsersInvalidPhoneIsRowFailure(t *testing.T) {
	db := newImportTestDB(t)
	content := []byte("username,password,phone\nbadphone,Abc12345!,123\ngoodphone,Abc12345!,13900000001\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceUsers,
		Format:   ImportFormatCSV,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("result = %d succeeded / %d failed, want 1/1 (details=%+v)",
			result.Succeeded, result.Failed, result.Details)
	}
}

// ─────────────────────────── 题库导入 ───────────────────────────

// CSV 与 JSON 两条路径都要能导入，且 hash 与写入侧公式一致（去重靠它）。
func TestImportDataQuestionsCSVAndJSON(t *testing.T) {
	db := newImportTestDB(t)
	// CSV 的转义规则是「引号双写」（不是反斜杠）：options=`[]`、answer=`["2"]`
	// 在 CSV 里写作 "" 包裹 + 内部引号双写 → "[]","[""2""]"。
	csvContent := []byte("question,type,options,answer,course_name\n" +
		"1+1=?,4,\"[]\",\"[\"\"2\"\"]\",数学\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatCSV,
		Content:  csvContent,
	})
	if err != nil {
		t.Fatalf("csv import: %v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("csv result = %+v, want 1 succeeded", result)
	}

	// 同一道题再导一次 → skipped（hash + course_name 已存在）。
	again, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatCSV,
		Content:  csvContent,
	})
	if err != nil {
		t.Fatalf("csv re-import: %v", err)
	}
	if again.Skipped != 1 || again.Succeeded != 0 {
		t.Fatalf("re-import = %+v, want 1 skipped", again)
	}

	// JSON 数组形态（前端导出的形态）。
	jsonContent := []byte(`[{"question":"2+2=?","type":4,"options":[],"answer":["4"],"course_name":"数学"}]`)
	jsonResult, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatJSON,
		Content:  jsonContent,
	})
	if err != nil {
		t.Fatalf("json import: %v", err)
	}
	if jsonResult.Succeeded != 1 {
		t.Fatalf("json result = %+v, want 1 succeeded", jsonResult)
	}

	// JSON 信封形态（/questions/import 的请求体）。
	envelope := []byte(`{"items":[{"question":"3+3=?","type":4,"options":[],"answer":["6"]}]}`)
	envelopeResult, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatJSON,
		Content:  envelope,
	})
	if err != nil {
		t.Fatalf("json envelope import: %v", err)
	}
	if envelopeResult.Succeeded != 1 {
		t.Fatalf("json envelope result = %+v, want 1 succeeded", envelopeResult)
	}

	var count int64
	if err := db.Model(&entity.Tiku{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Fatalf("tiku rows = %d, want 3", count)
	}
}

// options / answer 不是合法 JSON 数组 → 该行 failed，其余行照常导入。
func TestImportDataQuestionsInvalidJSONCellIsRowFailure(t *testing.T) {
	db := newImportTestDB(t)
	content := []byte("question,type,options,answer\n" +
		"bad,4,not-json,\"[]\"\n" +
		"good,4,\"[]\",\"[\"\"A\"\"]\"\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceQuestions,
		Format:   ImportFormatCSV,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Succeeded != 1 || result.Failed != 1 {
		t.Fatalf("result = %d/%d, want 1 succeeded 1 failed (details=%+v)",
			result.Succeeded, result.Failed, result.Details)
	}
}

// DryRun 只校验不落库。
func TestImportDataDryRunWritesNothing(t *testing.T) {
	db := newImportTestDB(t)
	content := []byte("value,reason\n13800000003,测试\n")
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource: ImportResourceBlacklist,
		Format:   ImportFormatCSV,
		Content:  content,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Succeeded != 1 {
		t.Fatalf("dry run result = %+v, want 1 succeeded", result)
	}
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("dry run must not write, got %d rows", count)
	}
}

// 导入模板必须只有表头，且列名与解析器一致（否则「下载模板 → 填 → 导入」直接失败）。
func TestImportDataTemplateHeaderMatchesParser(t *testing.T) {
	for _, resource := range ImportResourceList() {
		content, err := ImportTemplateCSV(resource)
		if err != nil {
			t.Fatalf("template %q: %v", resource, err)
		}
		header, rows, parseErr := ParseImportRows(ImportFormatCSV, content)
		// 模板没有数据行 → 解析器会返回 ErrImportFileEmpty，这是预期行为。
		if parseErr != nil && !errors.Is(parseErr, ErrImportFileEmpty) {
			t.Fatalf("template %q must be parseable: %v", resource, parseErr)
		}
		if len(rows) != 0 {
			t.Fatalf("template %q must have no data rows, got %d", resource, len(rows))
		}
		index := importColumnIndex(header)
		switch resource {
		case ImportResourceBlacklist:
			if err := requireImportColumns(index, []string{"value"}); err != nil {
				t.Fatalf("blacklist template must satisfy its own required columns: %v", err)
			}
		case ImportResourceUsers:
			if err := requireImportColumns(index, []string{"username", "password"}); err != nil {
				t.Fatalf("users template must satisfy its own required columns: %v", err)
			}
		case ImportResourceQuestions:
			if err := requireImportColumns(index, []string{"question"}); err != nil {
				t.Fatalf("questions template must satisfy its own required columns: %v", err)
			}
		}
	}
}

// 不支持的资源要明确报错（400 类），不静默当成题库。
func TestImportDataUnsupportedResource(t *testing.T) {
	if _, err := ImportDataWithDB(newImportTestDB(t), ImportDataOptions{
		Resource: "billing", Format: ImportFormatCSV, Content: []byte("a\n1\n"),
	}); !errors.Is(err, ErrImportResourceUnsupported) {
		t.Fatalf("err = %v, want ErrImportResourceUnsupported", err)
	}
}

// 空文件 / 只有表头 → ErrImportFileEmpty。
func TestImportDataEmptyFile(t *testing.T) {
	if _, err := ImportDataWithDB(newImportTestDB(t), ImportDataOptions{
		Resource: ImportResourceBlacklist, Format: ImportFormatCSV, Content: []byte("value\n"),
	}); !errors.Is(err, ErrImportFileEmpty) {
		t.Fatalf("err = %v, want ErrImportFileEmpty", err)
	}
}

// 弱密码判据必须与注册侧口径一致（8 位 + 大小写 + 数字 + 特殊字符）。
func TestImportDataIsWeakPassword(t *testing.T) {
	weak := []string{"", "abc", "12345678", "abcdefgh", "Abcdefg1", "Abc12345"}
	strong := []string{"Abc12345!", "Zz9!aaaa", "P@ssw0rd1"}
	for _, password := range weak {
		if !IsWeakImportPassword(password) {
			t.Fatalf("IsWeakImportPassword(%q) = false, want true", password)
		}
	}
	for _, password := range strong {
		if IsWeakImportPassword(password) {
			t.Fatalf("IsWeakImportPassword(%q) = true, want false", password)
		}
	}
}

// 脱敏函数逐个钉住（这些是 111 §4.5 的规格，改了就是泄漏或不可用）。
func TestExportCsvMaskHelpers(t *testing.T) {
	// 手机号：前 3 后 4
	if got := MaskExportPhone("13812345678"); got != "138****5678" {
		t.Fatalf("phone = %q, want 138****5678", got)
	}
	// 识别不出 → 全掩码（宁可丢信息，不可泄漏）
	if got := MaskExportPhone("not-a-phone"); strings.Contains(got, "not") {
		t.Fatalf("unrecognized phone must be fully masked, got %q", got)
	}
	// 邮箱：首字符 + 域名
	if got := MaskExportEmail("alice@example.com"); got != "a***@example.com" {
		t.Fatalf("email = %q, want a***@example.com", got)
	}
	if got := MaskExportEmail("bad-email"); !strings.Contains(got, "*") {
		t.Fatalf("unrecognized email must be masked, got %q", got)
	}
	// 微信：首尾各 3 位
	if got := MaskExportWechatID("oABC123xyz789"); got != "oAB*****789" {
		t.Fatalf("wechat = %q, want oAB*****789", got)
	}
	if got := MaskExportWechatID("short"); got != "*****" {
		t.Fatalf("short wechat must be fully masked, got %q", got)
	}
	// token：前 6 后 4
	if got := MaskExportToken("tk-9f3a12345678c871"); got != "tk-9f3****c871" {
		t.Fatalf("token = %q, want tk-9f3****c871", got)
	}
	if got := MaskExportToken("short"); !strings.HasPrefix(got, "*") {
		t.Fatalf("short token must be masked, got %q", got)
	}
	// IP：保留前两段
	if got := MaskExportIP("203.0.113.45"); got != "203.0.*.*" {
		t.Fatalf("ip = %q, want 203.0.*.*", got)
	}
	if got := MaskExportIP("2001:db8::1"); strings.Contains(got, "2001") {
		t.Fatalf("IPv6 must be fully masked, got %q", got)
	}
	// 空值保持空（不要把「没有」渲染成 ****）
	for _, mask := range []func(string) string{
		MaskExportPhone, MaskExportEmail, MaskExportWechatID, MaskExportToken, MaskExportIP,
	} {
		if got := mask(""); got != "" {
			t.Fatalf("empty value must stay empty, got %q", got)
		}
	}
}

// nowFunc 保证时间相关断言不依赖真实时钟（测试里只用它构造 createdAt）。
var _ = time.Now
