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
	mu            sync.Mutex
	files         [3]*rotatingFile
	thinking      *rotatingFile
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
	thinkingFile, err := openRotatingFile(dir, "Tiku-thinking", maxBytes)
	if err != nil {
		for _, opened := range s.files {
			if opened != nil {
				_ = opened.close()
			}
		}
		return fmt.Errorf("open Tiku-thinking log: %w", err)
	}
	s.thinking = thinkingFile
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

// AIThinkingWithSecrets writes a detailed AI trace while also redacting
// request-specific credentials that cannot be recognized by generic patterns.
func AIThinkingWithSecrets(text string, secrets ...string) {
	stateMu.Lock()
	s := active
	stateMu.Unlock()
	if s == nil {
		return
	}

	line := fmt.Sprintf("[AI-THINKING] %s | %s\n", time.Now().In(ShanghaiLocation()).Format("2006/01/02 - 15:04:05"), strings.TrimRight(RedactSecrets(text, secrets...), "\r\n"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.thinking == nil {
		return
	}
	if err := s.thinking.write([]byte(line)); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write AI thinking log: %v\n", err)
	}
	if err := pruneLogFiles(s.logDir, s.maxTotalBytes); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "清理超限日志失败 directory=%s error=%v\n", s.logDir, err)
	}
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

func isManagedLogFile(name string) bool {
	for _, prefix := range []string{"Tiku", "Tiku-warning", "Tiku-error", "Tiku-thinking"} {
		if name == prefix+".txt" || name == prefix+".txt.gz" || isManagedLogArchive(name) {
			return true
		}
	}
	return false
}

func isManagedLogArchive(name string) bool {
	for _, prefix := range []string{"Tiku", "Tiku-warning", "Tiku-error", "Tiku-thinking"} {
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
