package logger

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
	PanicLevel
)

const MaxLogFileBytes int64 = 10 * 1024 * 1024
const DefaultMaxTotalLogBytes int64 = 5 * 1024 * 1024 * 1024

// ThinkingLogSizeFactor 是 Tiku-thinking.txt 相对常规日志的单文件上限倍数。
//
// 为什么单独放宽（task_doc/45 §0.26.1、task_doc/77 §11.1）：
// 2026-11-11 三档并两档后，Tiku-thinking.txt 除了几百字节的编排轨迹，
// 还要装完整提示词与完整响应 —— 一轮搜题可能几十 KB，体积比拆分时
// 涨一到两个数量级。若仍用 10 MiB，一天就能滚出几十个归档文件，
// 排查「某轮到底发了什么」时要在多个 .gz 里翻。
//
// 取 6 倍 = 生产 60 MiB（落在 45 号文档建议的 32~64 MiB 区间）。
// **不动 MaxLogFileBytes 常量本身**：那会同时放大 Tiku / Tiku-error /
// Tiku-review-rebate，属于误伤；只有这一档需要更大。
// 用倍数而不是绝对字节数还有第二个好处：单测传小额度（如 1 KiB）时，
// 上限按同一比例放大，「写满即轮转」的行为仍可复现。
const ThinkingLogSizeFactor int64 = 6

// thinkingFileLimit 返回 Tiku-thinking.txt 的单文件上限。
func thinkingFileLimit(maxBytes int64) int64 {
	if maxBytes <= 0 {
		// setupLoggerWithLimits 会在更早处拒绝非正数，这里只做溢出防护。
		return maxBytes
	}
	if maxBytes > (1<<62)/ThinkingLogSizeFactor {
		return maxBytes
	}
	return maxBytes * ThinkingLogSizeFactor
}

var LogDir = flag.String("log-dir", "logs", "specify the log directory")

var (
	stateMu                sync.Mutex
	active                 *loggerState
	ginStatusPattern       = regexp.MustCompile(`^\[GIN\].*?\|\s*(\d{3})\s*\|`)
	ansiPattern            = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	logTimestampPattern    = regexp.MustCompile(`(\d{4})/(\d{2})/(\d{2})[ -]+(\d{2}):(\d{2}):(\d{2})`)
	logBatchArchivePattern = regexp.MustCompile(`^\d{8}_\d{6}(?:_\d{2})?\.zip$`)
)

type loggerState struct {
	mu    sync.Mutex
	files [3]*rotatingFile
	// thinking 是 AI 搜题通道：**编排轨迹 + 完整提示词 + 完整响应**。
	//
	// 原本完整报文单独写 Tiku-ai-payload.txt，用户 2026-11-11 定案「只要
	// Tiku-review-rebate.txt 与 Tiku-thinking.txt 两个，都保留完整提示词、
	// 回复记录等」（task_doc/45 §0.26.1、task_doc/77 §11.1），因此这一档
	// 现在既装结构化轨迹，也装完整请求体 / 响应体 / 来源 / 结果摘要。
	thinking *rotatingFile
	// reviewRebate 是评价返现打分的专用追踪（task_doc/73）。
	reviewRebate  *rotatingFile
	stdout        io.Writer
	stderr        io.Writer
	standard      io.Writer
	standardFlags int
	out           *outputWriter
	err           *outputWriter
	closed        bool
	logDir        string
	maxTotalBytes int64
}

type outputWriter struct {
	state *loggerState
	level Level
}

func SetupGinLog() error {
	return setupLoggerWithLimits(*LogDir, MaxLogFileBytes, DefaultMaxTotalLogBytes)
}

func setupLogger(dir string, maxBytes int64) error {
	return setupLoggerWithLimits(dir, maxBytes, DefaultMaxTotalLogBytes)
}

func setupLoggerWithLimits(dir string, maxBytes int64, maxTotalBytes int64) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("log directory must not be empty")
	}
	if maxBytes <= 0 {
		return errors.New("log file size limit must be positive")
	}
	if maxTotalBytes < 0 {
		return errors.New("total log size limit must not be negative")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if active != nil {
		if err := active.close(); err != nil {
			return err
		}
		active = nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if err := archiveExistingLogs(dir); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "归档旧日志失败 directory=%s error=%v\n", dir, err)
	}
	s := &loggerState{stdout: gin.DefaultWriter, stderr: gin.DefaultErrorWriter, standard: log.Writer(), standardFlags: log.Flags(), logDir: dir, maxTotalBytes: maxTotalBytes}
	for index, name := range []string{"Tiku", "Tiku-warning", "Tiku-error"} {
		file, err := openRotatingFile(dir, name, maxBytes)
		if err != nil {
			for _, opened := range s.files {
				if opened != nil {
					_ = opened.close()
				}
			}
			return fmt.Errorf("open %s log: %w", name, err)
		}
		s.files[index] = file
	}
	thinkingFile, err := openRotatingFile(dir, "Tiku-thinking", thinkingFileLimit(maxBytes))
	if err != nil {
		for _, opened := range s.files {
			if opened != nil {
				_ = opened.close()
			}
		}
		return fmt.Errorf("open Tiku-thinking log: %w", err)
	}
	s.thinking = thinkingFile
	reviewRebateFile, err := openRotatingFile(dir, "Tiku-review-rebate", maxBytes)
	if err != nil {
		for _, opened := range s.files {
			if opened != nil {
				_ = opened.close()
			}
		}
		if s.thinking != nil {
			_ = s.thinking.close()
		}
		return fmt.Errorf("open Tiku-review-rebate log: %w", err)
	}
	s.reviewRebate = reviewRebateFile
	if err := pruneLogFiles(dir, maxTotalBytes); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "清理超限日志失败 directory=%s error=%v\n", dir, err)
	}
	s.out = &outputWriter{state: s, level: InfoLevel}
	s.err = &outputWriter{state: s, level: ErrorLevel}
	gin.DefaultWriter = s.out
	gin.DefaultErrorWriter = s.err
	log.SetOutput(Writer(InfoLevel))
	log.SetFlags(0)
	active = s
	return nil
}

// SetMaxTotalLogBytes applies the persisted system setting without restarting the process.
func SetMaxTotalLogBytes(maxTotalBytes int64) error {
	if maxTotalBytes <= 0 {
		return errors.New("total log size limit must be positive")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if active == nil {
		return errors.New("logger is not initialized")
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	active.maxTotalBytes = maxTotalBytes
	if err := pruneLogFiles(active.logDir, maxTotalBytes); err != nil {
		return err
	}
	return nil
}

func Close() error { return closeLogger() }

func closeLogger() error {
	stateMu.Lock()
	defer stateMu.Unlock()
	if active == nil {
		return nil
	}
	err := active.close()
	active = nil
	return err
}

func (s *loggerState) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	var errs []error
	for _, file := range s.files {
		if file != nil {
			errs = append(errs, file.close())
		}
	}
	if s.thinking != nil {
		errs = append(errs, s.thinking.close())
	}
	if s.reviewRebate != nil {
		errs = append(errs, s.reviewRebate.close())
	}
	s.mu.Unlock()
	if gin.DefaultWriter == s.out {
		gin.DefaultWriter = s.stdout
	}
	if gin.DefaultErrorWriter == s.err {
		gin.DefaultErrorWriter = s.stderr
	}
	log.SetOutput(s.standard)
	log.SetFlags(s.standardFlags)
	return errors.Join(errs...)
}

func (w *outputWriter) Write(p []byte) (int, error) {
	s := w.state
	text := ansiPattern.ReplaceAllString(RedactSecrets(string(p)), "")
	level := classifyLevel(text, w.level)
	s.mu.Lock()
	defer s.mu.Unlock()
	console := s.stdout
	if level >= ErrorLevel {
		console = s.stderr
	}
	if console != nil {
		if _, err := io.WriteString(console, text); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write console log: %v\n", err)
		}
	}
	if !s.closed {
		for index, file := range s.files {
			if index == 1 && level < WarnLevel || index == 2 && level < ErrorLevel {
				continue
			}
			if err := file.write([]byte(text)); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "write %s log: %v\n", file.name, err)
			}
		}
		if err := pruneLogFiles(s.logDir, s.maxTotalBytes); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "清理超限日志失败 directory=%s error=%v\n", s.logDir, err)
		}
	}
	// Logging must not turn a successfully handled request into a business failure.
	return len(p), nil
}

func classifyLevel(text string, fallback Level) Level {
	if strings.HasPrefix(text, "[GIN-debug] [WARNING]") || strings.HasPrefix(text, "[WARNING]") {
		return WarnLevel
	}
	for level, prefix := range map[Level]string{
		DebugLevel: "[DEBUG]", InfoLevel: "[INFO]", WarnLevel: "[WARN]", ErrorLevel: "[ERROR]", FatalLevel: "[FATAL]", PanicLevel: "[PANIC]",
	} {
		if strings.HasPrefix(text, prefix) {
			return level
		}
	}
	if matches := ginStatusPattern.FindStringSubmatch(text); len(matches) == 2 {
		status, _ := strconv.Atoi(matches[1])
		if status >= 500 {
			return ErrorLevel
		}
		if status >= 400 {
			return WarnLevel
		}
	}
	return fallback
}

type levelWriter struct{ level Level }

func Writer(level Level) io.Writer { return levelWriter{level: level} }

func (w levelWriter) Write(p []byte) (int, error) {
	writeMessage(w.level, string(p))
	return len(p), nil
}

func writeMessage(level Level, text string) {
	labels := [...]string{"DEBUG", "INFO", "WARN", "ERROR", "FATAL", "PANIC"}
	if level < DebugLevel || level > PanicLevel {
		level = InfoLevel
	}
	writer := gin.DefaultWriter
	if level >= ErrorLevel {
		writer = gin.DefaultErrorWriter
	}
	_, _ = fmt.Fprintf(writer, "[%s] %s | %s\n", labels[level], time.Now().In(ShanghaiLocation()).Format("2006/01/02 - 15:04:05"), singleLineLogText(text))
}

// singleLineLogText 先把消息脱敏，再把内嵌换行转义成字面量 \n。
//
// 一行一事件是文件日志能被 grep / 解析的前提：上游 SSE 原文（data: {...}）、
// 多行 SQL、panic 堆栈都带换行，直接写出会让一个事件占多行、级别前缀只落在第一行，
// 线上实测 Tiku-error.txt 里混进了 68 行没有前缀的裸 data 片段。
func singleLineLogText(text string) string {
	message := strings.TrimRight(RedactSecrets(text), "\r\n")
	if !strings.ContainsAny(message, "\r\n") {
		return message
	}
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\r`).Replace(message)
}

// AIThinking writes the observable AI search trace to its dedicated file.
// It intentionally does not mirror the trace to the console or regular logs.
func AIThinking(text string) {
	AIThinkingWithSecrets(text)
}

// AIThinkingWithSecrets 把 AI 搜题通道的一条记录写进 Tiku-thinking.txt。
//
// 这一档同时承载**编排轨迹**（阶段/耗时/模型/状态等结构化字段）与
// **完整报文**（完整提示词 RequestBody、完整响应 FullResponse、来源、结果摘要）。
// 2026-11-11 用户定案取消 Tiku-ai-payload.txt，把完整报文并入本文件
// （task_doc/45 §0.26.1、task_doc/77 §11.1）；合并的代价是这一档体积
// 涨一到两个数量级，因此它的单文件上限由 thinkingFileLimit 单独放宽。
//
// secrets 用于抹掉调用方已知的具体凭据（如 APIKey）。
func AIThinkingWithSecrets(text string, secrets ...string) {
	writeDedicatedFile("thinking", "Tiku-thinking", "[AI-THINKING]", text, secrets...)
}

// writeDedicatedFile 是三类「独立日志文件」通道的共用写入路径。
//
// field 是 loggerState 上的字段名（便于错误信息定位是哪一档写失败），
// file 是文件名前缀，tag 是行首标记，text 是正文，secrets 用于定向脱敏。
//
// 三条不可协商的性质（三档共用）：
//   - 只写文件，不镜像到控制台或常规日志；
//   - 必过 RedactSecrets —— 调用方的错误/报文里可能带供应商地址与密钥；
//   - 写完立即 pruneLogFiles，避免这一档绕过总量上限。
//
// 拿不到 state 时静默返回：日志初始化失败不该让业务路径 panic。
func writeDedicatedFile(field, file, tag, text string, secrets ...string) {
	stateMu.Lock()
	s := active
	stateMu.Unlock()
	if s == nil {
		return
	}

	line := fmt.Sprintf("%s %s | %s\n", tag,
		time.Now().In(ShanghaiLocation()).Format("2006/01/02 - 15:04:05"),
		strings.TrimRight(RedactSecrets(text, secrets...), "\r\n"))

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	target := dedicatedFileOf(s, field)
	if target == nil {
		return
	}
	if err := target.write([]byte(line)); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write %s log: %v\n", file, err)
	}
	if err := pruneLogFiles(s.logDir, s.maxTotalBytes); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "清理超限日志失败 directory=%s error=%v\n", s.logDir, err)
	}
}

// dedicatedFileOf 按字段名取对应的轮转文件句柄。
//
// 用字符串分派而不是传 *rotatingFile：调用方在拿 state 之前
// 无法安全地解引用字段（state 可能为 nil，且字段本身可能尚未初始化）。
func dedicatedFileOf(s *loggerState, field string) *rotatingFile {
	switch field {
	case "thinking":
		return s.thinking
	case "reviewRebate":
		return s.reviewRebate
	default:
		return nil
	}
}

// ReviewRebateTrace writes the review-rebate Agent scoring trace to its own
// dedicated file (Tiku-review-rebate.txt), task_doc/73.
//
// 为什么不复用 AIThinking：
// AIThinking 写的是 Tiku-thinking.txt，那是 AI **搜题**的通道。评价返现的
// 判断过程混进去之后，两类日志都无法按主题检索 —— 排查「某条评价为什么被判
// 0.8 元」时要在整份搜题 trace 里翻。
//
// 与 AIThinking 一致的三条性质：
//   - 只写文件，不镜像到控制台或常规日志（trace 体量大，且含提示词全文）；
//   - 必过 RedactSecrets —— 模型错误里可能带供应商地址与密钥；
//   - 写完立即 pruneLogFiles，避免这个文件绕过总量上限。
//
// ⚠️ 新增本文件时必须同时改 isManagedLogFile 与 isManagedLogArchive 两处白名单，
// 否则轮转出的 .txt.N / .txt.gz 归档不再被 pruneLogFiles 识别，磁盘会无限增长。
func ReviewRebateTrace(text string, secrets ...string) {
	writeDedicatedFile("reviewRebate", "Tiku-review-rebate", "[REVIEW-REBATE]", text, secrets...)
}

var shanghaiLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		return location
	}
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}()

// ShanghaiLocation is the single timezone used by application logs and audit timestamps.
func ShanghaiLocation() *time.Location { return shanghaiLocation }

func archiveExistingLogs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(dir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return err
	}
	type source struct {
		name string
		path string
	}
	sources := make([]source, 0, len(entries))
	var earliest time.Time
	for _, entry := range entries {
		if entry.IsDir() || !isManagedLogFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		startedAt := logFileStartTime(path, info.ModTime())
		if earliest.IsZero() || startedAt.Before(earliest) {
			earliest = startedAt
		}
		sources = append(sources, source{name: entry.Name(), path: path})
	}
	if len(sources) == 0 {
		return nil
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	baseName := earliest.In(ShanghaiLocation()).Format("20060102_150405")
	archivePath := filepath.Join(archiveDir, baseName+".zip")
	for suffix := 1; ; suffix++ {
		if _, err := os.Lstat(archivePath); os.IsNotExist(err) {
			break
		} else if err != nil {
			return err
		}
		archivePath = filepath.Join(archiveDir, fmt.Sprintf("%s_%02d.zip", baseName, suffix))
	}
	tempPath := archivePath + ".tmp"
	_ = os.Remove(tempPath)
	output, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(output)
	for _, item := range sources {
		entryWriter, err := archive.Create(item.name)
		if err != nil {
			_ = archive.Close()
			_ = output.Close()
			_ = os.Remove(tempPath)
			return err
		}
		input, err := os.Open(item.path)
		if err != nil {
			_ = archive.Close()
			_ = output.Close()
			_ = os.Remove(tempPath)
			return err
		}
		_, copyErr := io.Copy(entryWriter, input)
		closeErr := input.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = archive.Close()
			_ = output.Close()
			_ = os.Remove(tempPath)
			return err
		}
	}
	if err := archive.Close(); err != nil {
		_ = output.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, archivePath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	for _, item := range sources {
		if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func logFileStartTime(path string, fallback time.Time) time.Time {
	file, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return fallback
	}
	matches := logTimestampPattern.FindStringSubmatch(string(content))
	if len(matches) != 7 {
		return fallback
	}
	value, err := time.ParseInLocation("2006/01/02 15:04:05", fmt.Sprintf("%s/%s/%s %s:%s:%s", matches[1], matches[2], matches[3], matches[4], matches[5], matches[6]), ShanghaiLocation())
	if err != nil {
		return fallback
	}
	return value
}

type logFileCandidate struct {
	path    string
	modTime time.Time
}

func pruneLogFiles(dir string, maxTotalBytes int64) error {
	if maxTotalBytes <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var total int64
	archives := make([]logFileCandidate, 0)
	for _, entry := range entries {
		if entry.IsDir() || !isManagedLogFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if isManagedLogArchive(entry.Name()) {
			archives = append(archives, logFileCandidate{path: filepath.Join(dir, entry.Name()), modTime: info.ModTime()})
		}
	}
	archiveDir := filepath.Join(dir, "archive")
	archiveEntries, archiveErr := os.ReadDir(archiveDir)
	if archiveErr == nil {
		for _, entry := range archiveEntries {
			if entry.IsDir() || !isManagedLogBatchArchive(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			archives = append(archives, logFileCandidate{path: filepath.Join(archiveDir, entry.Name()), modTime: info.ModTime()})
		}
	} else if !os.IsNotExist(archiveErr) {
		return archiveErr
	}
	sort.SliceStable(archives, func(i, j int) bool {
		if archives[i].modTime.Equal(archives[j].modTime) {
			return archives[i].path < archives[j].path
		}
		return archives[i].modTime.Before(archives[j].modTime)
	})
	for total > maxTotalBytes && len(archives) > 0 {
		oldest := archives[0]
		archives = archives[1:]
		info, statErr := os.Stat(oldest.path)
		if statErr == nil {
			total -= info.Size()
		}
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// managedLogPrefixes 是全部独立日志文件的名字前缀。
//
// ⚠️ 增删任何一个前缀都必须同步 isManagedLogFile 与 isManagedLogArchive
// **两处**（后者由前者调用，但两处都用这个切片，所以只改一处会漏）：
// 只在一个函数里登记，轮转出的 .txtN / .txtN.gz 归档就不再被 pruneLogFiles
// 识别，磁盘会无限增长（task_doc/73 记录过这个坑）。
//
// Tiku-ai-payload 已删除（2026-11-11 用户定案三档并两档，
// 完整报文并入 Tiku-thinking，见 task_doc/45 §0.26.1）。历史上轮转出的
// Tiku-ai-payload*.txt 因此不再被清理，需要人工删一次。
var managedLogPrefixes = []string{"Tiku", "Tiku-warning", "Tiku-error", "Tiku-thinking", "Tiku-review-rebate"}

func isManagedLogFile(name string) bool {
	for _, prefix := range managedLogPrefixes {
		if name == prefix+".txt" || name == prefix+".txt.gz" || isManagedLogArchive(name) {
			return true
		}
	}
	return false
}

func isManagedLogArchive(name string) bool {
	for _, prefix := range managedLogPrefixes {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(name, prefix)
		for _, suffix := range []string{".txt", ".txt.gz"} {
			digits := strings.TrimSuffix(remainder, suffix)
			if digits != remainder && digits != "" && strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
				return true
			}
		}
	}
	return false
}

func isManagedLogBatchArchive(name string) bool {
	return logBatchArchivePattern.MatchString(name)
}

func SysLog(s string)   { writeMessage(InfoLevel, s) }
func SysWarn(s string)  { writeMessage(WarnLevel, s) }
func SysError(s string) { writeMessage(ErrorLevel, s) }
func PanicLog(v ...any) { writeMessage(PanicLevel, fmt.Sprint(v...)) }

func FatalLog(v ...any) {
	writeMessage(FatalLevel, fmt.Sprint(v...))
	_ = Close()
	os.Exit(1)
}
