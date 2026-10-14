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
