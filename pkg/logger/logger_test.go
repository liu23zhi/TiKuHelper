package logger

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLoggerRoutesMessagesByLevel(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024*1024); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	SysLog("info-entry")
	SysWarn("warning-entry")
	SysError("error-entry")
	if err := closeLogger(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	all := readLoggerFile(t, dir, "Tiku.txt")
	warnings := readLoggerFile(t, dir, "Tiku-warning.txt")
	errors := readLoggerFile(t, dir, "Tiku-error.txt")
	for _, value := range []string{"info-entry", "warning-entry", "error-entry"} {
		if !strings.Contains(all, value) {
			t.Fatalf("all log is missing %q: %s", value, all)
		}
	}
	if strings.Contains(warnings, "info-entry") || !strings.Contains(warnings, "warning-entry") || !strings.Contains(warnings, "error-entry") {
		t.Fatalf("warning log has wrong level routing: %s", warnings)
	}
	if strings.Contains(errors, "info-entry") || strings.Contains(errors, "warning-entry") || !strings.Contains(errors, "error-entry") {
		t.Fatalf("error log has wrong level routing: %s", errors)
	}
}

// 一行一事件：多行消息（上游 SSE 原文 / 多行 SQL / 堆栈）必须转义换行，
// 否则一个事件占多行、只有第一行带级别前缀，文件日志无法逐行解析。
func TestLoggerEscapesEmbeddedNewlines(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024*1024); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	SysError("upstream failed\r\ndata: {\"error\":\"busy\"}\nnext-sql-line")
	if err := closeLogger(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	content := readLoggerFile(t, dir, "Tiku-error.txt")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one log line, got %d:\n%s", len(lines), content)
	}
	if !strings.HasPrefix(lines[0], "[ERROR] ") {
		t.Fatalf("expected the level prefix on the single line, got %q", lines[0])
	}
	if !strings.Contains(lines[0], `\ndata: {"error":"busy"}`) || !strings.Contains(lines[0], `\nnext-sql-line`) {
		t.Fatalf("expected embedded newlines to be escaped, got %q", lines[0])
	}
	if strings.Contains(content, "\ndata: ") {
		t.Fatalf("raw newline leaked into the log file:\n%s", content)
	}
}

func TestAIThinkingUsesDedicatedRotatingFileAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024*1024); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	AIThinking("模型第1轮 requestId=req-thinking 剩余模型调用=2 搜索=1 Authorization: Bearer thinking-secret")
	if err := closeLogger(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	thinking := readLoggerFile(t, dir, "Tiku-thinking.txt")
	if !strings.Contains(thinking, "模型第1轮") || !strings.Contains(thinking, "[AI-THINKING]") {
		t.Fatalf("dedicated thinking log is missing the trace: %s", thinking)
	}
	if strings.Contains(thinking, "thinking-secret") {
		t.Fatalf("dedicated thinking log leaked a secret: %s", thinking)
	}
	if strings.Contains(readLoggerFile(t, dir, "Tiku.txt"), "模型第1轮") {
		t.Fatal("thinking trace should not be written to the general log")
	}
}

func TestLoggerArchivesOldestFileAsSuffix01(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 90); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	for index := 1; index <= 8; index++ {
		SysLog(strings.Repeat("x", 24) + "-entry-" + string(rune('0'+index)))
		time.Sleep(time.Millisecond)
	}
	if err := closeLogger(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	first := readLoggerFile(t, dir, "Tiku01.txt")
	second := readLoggerFile(t, dir, "Tiku02.txt")
	if !strings.Contains(first, "entry-1") {
		t.Fatalf("oldest archive Tiku01.txt should contain first entry: %s", first)
	}
	if !strings.Contains(second, "entry-2") {
		t.Fatalf("second archive Tiku02.txt should contain second entry: %s", second)
	}
	if _, err := os.Stat(filepath.Join(dir, "Tiku.txt")); err != nil {
		t.Fatalf("active log without numeric suffix is missing: %v", err)
	}
}

func readLoggerFile(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content, err := os.ReadFile(path)
	if err == nil {
		return string(content)
	}
	t.Fatalf("read %s: %v", name, err)
	return ""
}

func readLogArchive(t *testing.T, dir string) (string, map[string]string) {
	t.Helper()
	archives, err := filepath.Glob(filepath.Join(dir, "archive", "*.zip"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one log archive, got %v (err=%v)", archives, err)
	}
	reader, err := zip.OpenReader(archives[0])
	if err != nil {
		t.Fatalf("open log archive: %v", err)
	}
	defer reader.Close()
	entries := make(map[string]string, len(reader.File))
	for _, item := range reader.File {
		file, err := item.Open()
		if err != nil {
			t.Fatalf("open archive entry %s: %v", item.Name, err)
		}
		content, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			t.Fatalf("read archive entry %s: %v", item.Name, readErr)
		}
		entries[item.Name] = string(content)
	}
	return filepath.Base(archives[0]), entries
}

func TestLoggerArchivesPreviousLogsAsSingleZipOnStartup(t *testing.T) {
	dir := t.TempDir()
	for name, item := range map[string]string{
		"Tiku.txt":         "[INFO] 2026/10/13 - 08:05:00 | 上一次启动的全部日志\n",
		"Tiku-warning.txt": "[WARN] 2026/10/13 - 08:00:00 | 上一次启动的警告日志\n",
		"Tiku-error.txt":   "[ERROR] 2026/10/13 - 08:10:00 | 上一次启动的错误日志\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(item), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := setupLoggerWithLimits(dir, 1024, 1024*1024); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	archiveName, entries := readLogArchive(t, dir)
	if archiveName != "20261013_080000.zip" {
		t.Fatalf("archive name should use earliest log start time, got %s", archiveName)
	}
	for _, name := range []string{"Tiku.txt", "Tiku-warning.txt", "Tiku-error.txt"} {
		if !strings.Contains(entries[name], "上一次启动") {
			t.Fatalf("startup archive lost %s content: %s", name, entries[name])
		}
		if got := readLoggerFile(t, dir, name); got != "" {
			t.Fatalf("active %s log should start empty after startup archive: %q", name, got)
		}
	}
}

func TestLoggerPrunesOldestArchivesWhenTotalLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	oldContent := make([]byte, 256)
	newContent := make([]byte, 256)
	for i := range oldContent {
		oldContent[i] = byte(i)
		newContent[i] = byte(255 - i)
	}
	archiveDir := filepath.Join(dir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(archiveDir, "20260101_000000.zip")
	newPath := filepath.Join(archiveDir, "20260102_000000.zip")
	if err := os.WriteFile(oldPath, oldContent, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newContent, 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := setupLoggerWithLimits(dir, 1024, 500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("oldest archive should be removed after total limit pruning, err=%v", err)
	}
	if got, err := os.ReadFile(newPath); err != nil || len(got) != len(newContent) {
		t.Fatalf("newest archive should be kept, got %d bytes", len(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "Tiku.txt")); err != nil {
		t.Fatalf("active log must remain after pruning: %v", err)
	}
}

func TestLoggerUsesShanghaiTime(t *testing.T) {
	location := ShanghaiLocation()
	if location == nil || location.String() != "Asia/Shanghai" {
		t.Fatalf("expected Asia/Shanghai location, got %v", location)
	}
	_, offset := time.Now().In(location).Zone()
	if offset != 8*60*60 {
		t.Fatalf("expected Shanghai UTC+8 offset, got %d", offset)
	}
}

func TestLoggerArchivesAllPreviousLogsAndStartsFreshAfterRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Tiku01.txt"), []byte("oldest"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiku07.txt"), []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiku.txt"), []byte(strings.Repeat("a", 90)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := setupLogger(dir, 100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	SysLog("new-entry")
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(filepath.Join(dir, "archive", "*.zip"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one startup archive, got %v (err=%v)", archives, err)
	}
	firstReader, err := zip.OpenReader(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	var firstArchiveText strings.Builder
	for _, item := range firstReader.File {
		file, openErr := item.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		content, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		firstArchiveText.Write(content)
	}
	_ = firstReader.Close()
	if !strings.Contains(firstArchiveText.String(), "oldest") || !strings.Contains(firstArchiveText.String(), "previous") {
		t.Fatalf("startup archive did not contain all previous logs: %s", firstArchiveText.String())
	}
	if !strings.Contains(readLoggerFile(t, dir, "Tiku.txt"), "new-entry") {
		t.Fatal("new log entry missing")
	}
	if err := setupLogger(dir, 1024); err != nil {
		t.Fatal(err)
	}
	SysLog("restart-entry")
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	archives, err = filepath.Glob(filepath.Join(dir, "archive", "*.zip"))
	if err != nil || len(archives) != 2 {
		t.Fatalf("second startup should create a second batch archive, got %v (err=%v)", archives, err)
	}
	if active := readLoggerFile(t, dir, "Tiku.txt"); !strings.Contains(active, "restart-entry") || strings.Contains(active, "new-entry") {
		t.Fatalf("restart should start a fresh active log: %s", active)
	}
}

func TestLoggerRoutesGinStandardAndFixedLevelWriters(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024*1024); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	log.Print("standard-library-info")
	_, _ = io.WriteString(gin.DefaultWriter, "[GIN-debug] [WARNING] gin-warning\n")
	_, _ = io.WriteString(Writer(WarnLevel), "gorm-warning")
	_, _ = io.WriteString(Writer(ErrorLevel), "gorm-error")
	_, _ = io.WriteString(Writer(FatalLevel), "fatal-entry")
	_, _ = io.WriteString(Writer(PanicLevel), "panic-entry")
	PanicLog("panic-function-entry")
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.GET("/ok", func(c *gin.Context) { c.Status(200) })
	router.GET("/bad", func(c *gin.Context) { c.Status(400) })
	router.GET("/broken", func(c *gin.Context) { c.Status(500) })
	router.GET("/panic", func(c *gin.Context) { panic("recovery-entry") })
	for _, route := range []string{"/ok", "/bad", "/broken", "/panic"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", route, nil))
	}
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	all := readLoggerFile(t, dir, "Tiku.txt")
	warnings := readLoggerFile(t, dir, "Tiku-warning.txt")
	errors := readLoggerFile(t, dir, "Tiku-error.txt")
	for _, text := range []string{"standard-library-info", "gorm-warning", "gorm-error", "fatal-entry", "panic-entry", "panic-function-entry", "recovery-entry", "/ok", "/bad", "/broken"} {
		if !strings.Contains(all, text) {
			t.Fatalf("all log missing %q", text)
		}
	}
	for _, text := range []string{"gin-warning", "gorm-warning", "gorm-error", "fatal-entry", "panic-entry", "recovery-entry", "/bad", "/broken"} {
		if !strings.Contains(warnings, text) {
			t.Fatalf("warning log missing %q", text)
		}
	}
	for _, text := range []string{"gorm-error", "fatal-entry", "panic-entry", "recovery-entry", "/broken"} {
		if !strings.Contains(errors, text) {
			t.Fatalf("error log missing %q", text)
		}
	}
	if strings.Contains(warnings, "standard-library-info") || strings.Contains(warnings, `"/ok"`) || strings.Contains(errors, "gorm-warning") || strings.Contains(errors, `"/bad"`) {
		t.Fatal("messages routed below threshold")
	}
	if strings.Contains(all, "\x1b[") {
		t.Fatal("text log contains ANSI terminal escapes")
	}
}

func TestLoggerDoesNotSplitOversizeEntriesOrOverwriteArchives(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	entry := strings.Repeat("z", 200)
	SysLog(entry)
	if err := os.WriteFile(filepath.Join(dir, "Tiku01.txt"), []byte("external archive"), 0644); err != nil {
		t.Fatal(err)
	}
	SysLog("next")
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	if got := readLoggerFile(t, dir, "Tiku01.txt"); got != "external archive" {
		t.Fatalf("existing archive overwritten: %s", got)
	}
	if got := readLoggerFile(t, dir, "Tiku02.txt"); !strings.Contains(got, entry) {
		t.Fatalf("oversized entry split: %s", got)
	}
}

func TestLoggerConcurrentWritesAndIndependentRotation(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	var writers sync.WaitGroup
	for i := 0; i < 20; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			SysLog(fmt.Sprintf("concurrent-entry-%02d", i))
		}(i)
	}
	writers.Wait()
	SysError("independent-error")
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, "Tiku*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, path := range entries {
		if strings.Contains(filepath.Base(path), "-") {
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(contents)
	}
	for i := 0; i < 20; i++ {
		if strings.Count(all.String(), fmt.Sprintf("concurrent-entry-%02d", i)) != 1 {
			t.Fatalf("missing or duplicate concurrent entry %d", i)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Tiku-warning01.txt")); !os.IsNotExist(err) {
		t.Fatalf("warning log rotated due to unrelated info entries: %v", err)
	}
	if !strings.Contains(readLoggerFile(t, dir, "Tiku-error.txt"), "independent-error") {
		t.Fatal("error entry missing")
	}
}

func TestLoggerPreservesGinCaptureAndRedactsSecrets(t *testing.T) {
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	var stdout, stderr bytes.Buffer
	gin.DefaultWriter, gin.DefaultErrorWriter = &stdout, &stderr
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })
	SysLog("capture-info")
	SysError("capture-error Authorization: Bearer sk-sensitive uapi-sensitive token=token-sensitive")
	if !strings.Contains(stdout.String(), "capture-info") || !strings.Contains(stderr.String(), "capture-error") {
		t.Fatal("legacy Gin writer capture bypassed")
	}
	for _, secret := range []string{"sk-sensitive", "uapi-sensitive", "token-sensitive"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("secret exposed: %s", stderr.String())
		}
	}
	redacted := RedactSecrets(`error Authorization: Bearer arbitrary-secret api_key="other-secret" opaque-value`, "opaque-value")
	for _, secret := range []string{"arbitrary-secret", "other-secret", "opaque-value"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret not redacted: %s", redacted)
		}
	}
	redacted = RedactSecrets(`GET /search?key=plain-key&icodefToken=plain-token Authorization: Basic user-pass https://user:pass@example.test/db?token=encoded%20token`)
	for _, secret := range []string{"plain-key", "plain-token", "user-pass", "user:pass", "encoded%20token"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("request secret not redacted: %s", redacted)
		}
	}
}

func TestLoggerSkipsArchiveDirectoryAndKeepsWritingAfterRotationFailure(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	if err := os.Mkdir(filepath.Join(dir, "Tiku01.txt"), 0755); err != nil {
		t.Fatal(err)
	}
	SysLog("first-entry")
	SysLog("second-entry")
	if !strings.Contains(readLoggerFile(t, dir, "Tiku02.txt"), "first-entry") {
		t.Fatal("rotation did not skip blocked archive path")
	}
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("block"), 0644); err != nil {
		t.Fatal(err)
	}
	active.files[0].dir = blocked
	SysLog("rotation-failed-but-recorded")
	if !strings.Contains(readLoggerFile(t, dir, "Tiku.txt"), "rotation-failed-but-recorded") {
		t.Fatal("rotation error dropped log instead of appending original file")
	}
}

func TestLoggerDiskWriteFailureDoesNotPropagate(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLogger() })
	if err := active.files[0].file.Close(); err != nil {
		t.Fatal(err)
	}
	message := []byte("[ERROR] disk-failure-entry\n")
	n, err := gin.DefaultErrorWriter.Write(message)
	if n != len(message) || err != nil {
		t.Fatalf("logging error leaked into business writer: n=%d err=%v", n, err)
	}
	if !strings.Contains(readLoggerFile(t, dir, "Tiku-error.txt"), "disk-failure-entry") {
		t.Fatal("failure in one log stopped independent logs")
	}
}

func TestFatalLogFlushesBeforeExit(t *testing.T) {
	if dir := os.Getenv("TIKU_LOG_FATAL_TEST_DIR"); dir != "" {
		if err := setupLogger(dir, 1024); err != nil {
			os.Exit(2)
		}
		FatalLog("fatal-before-exit")
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFatalLogFlushesBeforeExit$")
	cmd.Env = append(os.Environ(), "TIKU_LOG_FATAL_TEST_DIR="+dir)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("fatal did not exit with code 1: %v %s", err, output)
	}
	for _, name := range []string{"Tiku.txt", "Tiku-warning.txt", "Tiku-error.txt"} {
		if !strings.Contains(readLoggerFile(t, dir, name), "fatal-before-exit") {
			t.Fatalf("fatal entry absent from %s", name)
		}
	}
}

// task_doc/73 §3.2：评价返现 trace 必须写进**独立的**文件，
// 不混进 Tiku-thinking.txt（那是 AI 搜题通道），也不进常规日志。
func TestReviewRebateTraceUsesDedicatedFileAndRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := setupLogger(dir, 1024*1024); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	ReviewRebateTrace(`{"claimId":7,"outcome":"ok"} Authorization: Bearer review-secret`)
	if err := closeLogger(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	trace := readLoggerFile(t, dir, "Tiku-review-rebate.txt")
	if !strings.Contains(trace, `"claimId":7`) || !strings.Contains(trace, "[REVIEW-REBATE]") {
		t.Fatalf("dedicated review-rebate log is missing the trace: %s", trace)
	}
	if strings.Contains(trace, "review-secret") {
		t.Fatalf("review-rebate trace leaked a secret: %s", trace)
	}
	// 三条隔离断言：不能写进搜题通道、不能写进常规日志。
	if strings.Contains(readLoggerFile(t, dir, "Tiku-thinking.txt"), "claimId") {
		t.Fatal("review-rebate trace must not enter Tiku-thinking.txt")
	}
	if strings.Contains(readLoggerFile(t, dir, "Tiku.txt"), "claimId") {
		t.Fatal("review-rebate trace must not enter the general log")
	}
}

// 新日志文件必须被 pruneLogFiles 识别（两处白名单都要加），
// 否则它的轮转归档永远不会被清理，磁盘会无限增长。
func TestReviewRebateTraceFileIsManaged(t *testing.T) {
	for _, name := range []string{
		"Tiku-review-rebate.txt",
		"Tiku-review-rebate.txt.gz",
		"Tiku-review-rebate01.txt",
		"Tiku-review-rebate01.txt.gz",
	} {
		if !isManagedLogFile(name) {
			t.Fatalf("%s must be recognized as a managed log file", name)
		}
	}
}

// 三档并两档（task_doc/45 §0.26.1）之后，两份保留文件的**轮转归档**
// 必须仍然被 isManagedLogArchive 认出来，而被取消的 Tiku-ai-payload 必须不再被认。
//
// 为什么这条断言必须存在：只删 ai-payload 却顺手把 Tiku-thinking 从
// 归档白名单里漏掉时，一切编译通过、一切运行正常，只有「轮转出的
// Tiku-thinking01.txt 永远不被 prune 清理」这一条在悄悄失效 ——
// 磁盘会无限增长（task_doc/73 记录过同一个坑）。
// 这里逐条断言 isManagedLogArchive 的返回值，正是为了让那种残缺实现变红。
func TestManagedLogArchiveWhitelistAfterConsolidation(t *testing.T) {
	for _, name := range []string{
		"Tiku-thinking01.txt",
		"Tiku-thinking01.txt.gz",
		"Tiku-review-rebate01.txt",
		"Tiku-review-rebate01.txt.gz",
		"Tiku01.txt",
	} {
		if !isManagedLogArchive(name) {
			t.Fatalf("%s must stay recognized as a managed log archive", name)
		}
	}
	// Tiku-ai-payload 已取消：不再登记，也不再清理（历史遗留文件需人工删一次）。
	for _, name := range []string{
		"Tiku-ai-payload.txt",
		"Tiku-ai-payload.txt.gz",
		"Tiku-ai-payload01.txt",
		"Tiku-ai-payload01.txt.gz",
	} {
		if isManagedLogFile(name) || isManagedLogArchive(name) {
			t.Fatalf("%s must no longer be a managed log file after consolidation", name)
		}
	}
}

// 合并 ai-payload 后 Tiku-thinking.txt 体积涨一到两个数量级，
// 这一档的单文件上限必须比常规日志大一档，且**不能**顺带放大其它文件。
func TestThinkingLogUsesLargerFileLimit(t *testing.T) {
	// 生产常量本身不允许被改：只放宽 thinking 这一档。
	if MaxLogFileBytes != 10*1024*1024 {
		t.Fatalf("MaxLogFileBytes must stay 10 MiB, got %d", MaxLogFileBytes)
	}
	if limit := thinkingFileLimit(MaxLogFileBytes); limit < 32*1024*1024 {
		t.Fatalf("thinking log limit = %d bytes, want >= 32 MiB (task_doc/45 §0.26.1)", limit)
	}

	dir := t.TempDir()
	base := int64(1024)
	if err := setupLogger(dir, base); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	stateMu.Lock()
	thinkingLimit := active.thinking.maxBytes
	generalLimit := active.files[0].maxBytes
	stateMu.Unlock()
	if thinkingLimit != thinkingFileLimit(base) || thinkingLimit <= base {
		t.Fatalf("thinking file limit = %d, want %d (> base %d)", thinkingLimit, thinkingFileLimit(base), base)
	}
	if generalLimit != base {
		t.Fatalf("general log limit = %d, want untouched base %d", generalLimit, base)
	}

	// 行为验证：写入量已超过**常规档**上限、但仍在 thinking 上限内 → 不许轮转。
	AIThinking(strings.Repeat("a", int(base)+1024))
	if _, err := os.Stat(filepath.Join(dir, "Tiku-thinking01.txt")); !os.IsNotExist(err) {
		t.Fatalf("thinking log rotated below its own limit: %v", err)
	}
	// 再写一批超过 thinking 上限 → 必须轮转（证明上限是生效的，不是被无限放大）。
	AIThinking(strings.Repeat("b", int(thinkingLimit)))
	if _, err := os.Stat(filepath.Join(dir, "Tiku-thinking01.txt")); err != nil {
		t.Fatalf("thinking log did not rotate at its limit: %v", err)
	}
}

// 按年龄清理：两个 mtime 不同、内容大小相同的归档，一个超龄一个不超龄，
// 断言只删超龄的那一个。
//
// # 为什么必须放一个「不超龄」的对照文件
//
// 只造一个超龄归档时，「无脑删掉目录下所有归档」的实现也能过 ——
// 那条用例根本区分不出「按 mtime 判定」与「全删」。必须同时放对照文件，
// 且**两者大小相同**：这样「不超龄的必须还在且内容逐字节未变」这条断言
// 就不可能被「删了再写个空文件」蒙过去。
//
// # 为什么两个白名单各造一对
//
// 轮转归档（isManagedLogArchive，Tiku01.txt.gz）在日志目录下，
// 批量包（isManagedLogBatchArchive，20260101_000000.zip）在 archive/ 子目录下。
// 只扫其中一个的实现能过「只测另一个位置」的用例 —— 所以这里两个位置
// 各造一对（超龄 + 不超龄），并断言四个文件的最终存活状态。
func TestPruneLogFilesByAgeDeletesOnlyExpiredArchives(t *testing.T) {
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 两份内容长度完全相同（32 字节），避免「按大小猜」的实现混过断言。
	expiredBody := strings.Repeat("A", 32)
	freshBody := strings.Repeat("B", 32)

	// ① 日志目录下的轮转归档（isManagedLogArchive）。
	expiredRotated := filepath.Join(dir, "Tiku01.txt.gz")
	freshRotated := filepath.Join(dir, "Tiku02.txt.gz")
	// ② archive/ 下的批量包（isManagedLogBatchArchive）。
	expiredBatch := filepath.Join(archiveDir, "20260101_000000.zip")
	freshBatch := filepath.Join(archiveDir, "20260102_000000.zip")

	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour) // 10 天前
	recent := now.Add(-1 * 24 * time.Hour)
	for _, item := range []struct {
		path string
		body string
		when time.Time
	}{
		{expiredRotated, expiredBody, old},
		{expiredBatch, expiredBody, old},
		{freshRotated, freshBody, recent},
		{freshBatch, freshBody, recent},
	} {
		if err := os.WriteFile(item.path, []byte(item.body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(item.path, item.when, item.when); err != nil {
			t.Fatal(err)
		}
	}

	// 保留 3 天：10 天前的两份超龄，1 天前的两份不超龄。
	//
	// 这里直接调 pruneLogFilesByAge 而不是走 setupLoggerWithRetention：
	// setup 会先跑 archiveExistingLogs，把日志目录下的 .txt.gz 打包进
	// archive/，本轮测试的前置文件会被提前搬走，断言就失去意义。
	// 「setup 是否真的会调这个函数」由下面单独的接线用例负责
	// （TestStartupRetentionPrunesExpiredBatchArchives）——
	// 两件事分开测，各自的目的才清楚。
	if err := pruneLogFilesByAge(dir, 3); err != nil {
		t.Fatalf("prune by age: %v", err)
	}

	// 超龄的两份必须被删 —— 这一对同时覆盖两个白名单。
	for _, path := range []string{expiredRotated, expiredBatch} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired archive should be removed: %s err=%v", filepath.Base(path), err)
		}
	}
	// 不超龄的两份必须还在，且内容逐字节未变。
	for _, path := range []string{freshRotated, freshBatch} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("fresh archive should be kept: %s err=%v", filepath.Base(path), err)
		}
		if string(got) != freshBody {
			t.Fatalf("fresh archive content changed: %s got=%d bytes", filepath.Base(path), len(got))
		}
	}
}

// 0 天 = 关闭，而不是「删光」。
//
// 这是不可逆操作的口径约定：一次误配把 0 理解成「零天保留」，
// 会让升级后第一次清理就把全部历史归档删掉，且无法恢复。
func TestPruneLogFilesByAgeDisabledKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Tiku01.txt.gz")
	if err := os.WriteFile(path, []byte("keep-me"), 0644); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(path, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{0, -1} {
		if err := pruneLogFilesByAge(dir, days); err != nil {
			t.Fatalf("prune by age %d: %v", days, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retention=%d must not delete anything: %v", days, err)
		}
	}
}

// 启动路径真的会按年龄清理 —— 这是**接线**断言，不是函数单元断言。
//
// 为什么必须单独有这一条：上面 TestPruneLogFilesByAgeDeletesOnlyExpiredArchives
// 直接调 pruneLogFilesByAge，它只能证明「这个函数能删超龄文件」，
// 证明不了「setupLoggerWithRetention 会调它」。实测把 setup 里那次调用删掉，
// 直接调函数的用例照样全绿 —— 那就是假钉子。
//
// 用 archive/ 下的批量包做探针而不是日志目录下的 .txt.gz：
// setupLoggerWithRetention 会先调 archiveExistingLogs 把日志目录下的
// 受管文件打包搬进 archive/，放在日志目录的前置文件会被提前搬走。
// archive/ 里已经存在的 .zip 不会被 archiveExistingLogs 碰，是干净的探针。
func TestStartupRetentionPrunesExpiredBatchArchives(t *testing.T) {
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(archiveDir, "20250101_000000.zip")
	fresh := filepath.Join(archiveDir, "20260102_000000.zip")
	now := time.Now()
	for _, item := range []struct {
		path string
		when time.Time
	}{
		{expired, now.Add(-40 * 24 * time.Hour)},
		{fresh, now.Add(-1 * 24 * time.Hour)},
	} {
		if err := os.WriteFile(item.path, []byte("archive-body"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(item.path, item.when, item.when); err != nil {
			t.Fatal(err)
		}
	}

	// 保留 7 天；总量上限给足，确保「删掉的只可能是按年龄那条路径删的」。
	if err := setupLoggerWithRetention(dir, 1024*1024, 64*1024*1024, 7); err != nil {
		t.Fatalf("setup logger with retention: %v", err)
	}
	t.Cleanup(func() { _ = closeLogger() })

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("startup retention must delete the 40-day-old batch archive, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("startup retention must keep the 1-day-old batch archive: %v", err)
	}
}

// 白名单之外的旧文件不许被按年龄清理碰到。//
// Tiku-ai-payload 已停止管理（2026-11-11 三档并两档），历史遗留文件需要
// 人工处置。按年龄清理如果放宽成「目录下所有文件」，会把这些悄悄删掉。
func TestPruneLogFilesByAgeIgnoresUnmanagedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0755); err != nil {
		t.Fatal(err)
	}
	unmanaged := []string{
		filepath.Join(dir, "Tiku-ai-payload01.txt.gz"),
		filepath.Join(dir, "notes.txt"),
		filepath.Join(dir, "archive", "handwritten.zip"),
	}
	ancient := time.Now().Add(-365 * 24 * time.Hour)
	for _, path := range unmanaged {
		if err := os.WriteFile(path, []byte("unmanaged"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, ancient, ancient); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneLogFilesByAge(dir, 1); err != nil {
		t.Fatalf("prune by age: %v", err)
	}
	for _, path := range unmanaged {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unmanaged file must not be pruned by age: %s err=%v", filepath.Base(path), err)
		}
	}
}
