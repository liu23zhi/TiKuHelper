package logger

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
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

var LogDir = flag.String("log-dir", "logs", "specify the log directory")

var (
	stateMu          sync.Mutex
	active           *loggerState
	ginStatusPattern = regexp.MustCompile(`^\[GIN\].*?\|\s*(\d{3})\s*\|`)
	ansiPattern      = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

type loggerState struct {
	mu            sync.Mutex
	files         [3]*rotatingFile
	stdout        io.Writer
	stderr        io.Writer
	standard      io.Writer
	standardFlags int
	out           *outputWriter
	err           *outputWriter
	closed        bool
}

type outputWriter struct {
	state *loggerState
	level Level
}

func SetupGinLog() error {
	return setupLogger(*LogDir, MaxLogFileBytes)
}

func setupLogger(dir string, maxBytes int64) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("log directory must not be empty")
	}
	if maxBytes <= 0 {
		return errors.New("log file size limit must be positive")
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
	s := &loggerState{stdout: gin.DefaultWriter, stderr: gin.DefaultErrorWriter, standard: log.Writer(), standardFlags: log.Flags()}
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
	s.out = &outputWriter{state: s, level: InfoLevel}
	s.err = &outputWriter{state: s, level: ErrorLevel}
	gin.DefaultWriter = s.out
	gin.DefaultErrorWriter = s.err
	log.SetOutput(Writer(InfoLevel))
	log.SetFlags(0)
	active = s
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
	_, _ = fmt.Fprintf(writer, "[%s] %s | %s\n", labels[level], time.Now().Format("2006/01/02 - 15:04:05"), strings.TrimRight(RedactSecrets(text), "\r\n"))
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
