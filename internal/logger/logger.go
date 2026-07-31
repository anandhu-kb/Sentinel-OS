// Package logger provides a structured, multi-output debug logger for Sentinel-OS.
// It writes timestamped, color-coded log entries to both a rotating log file
// and to stdout, so you can see everything in the terminal and replay it later.
// It also maintains a fixed-size ring buffer (max 500 entries) so the UI can
// display live logs without consuming unbounded memory.
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ringBufferSize is the maximum number of log entries kept in memory.
// Old entries are silently discarded when the buffer is full.
const ringBufferSize = 500

// Level represents the severity of a log entry.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	case FATAL:
		return "FATAL"
	default:
		return "???"
	}
}

// ANSI color codes for terminal output
const (
	colorReset  = "\033[0m"
	colorDebug  = "\033[36m" // Cyan
	colorInfo   = "\033[32m" // Green
	colorWarn   = "\033[33m" // Yellow
	colorError  = "\033[31m" // Red
	colorFatal  = "\033[35m" // Magenta
	colorTime   = "\033[90m" // Dark gray
	colorCaller = "\033[34m" // Blue
)

func levelColor(l Level) string {
	switch l {
	case DEBUG:
		return colorDebug
	case INFO:
		return colorInfo
	case WARN:
		return colorWarn
	case ERROR:
		return colorError
	case FATAL:
		return colorFatal
	default:
		return colorReset
	}
}

// LogEntry is a single log record that can be serialized and sent to the frontend.
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Caller  string `json:"caller"`
	Message string `json:"message"`
}

// Logger is the core logger instance.
type Logger struct {
	mu      sync.Mutex
	fileLog *log.Logger
	logFile *os.File

	// ring buffer — fixed-size circular array, never grows
	ring  [ringBufferSize]LogEntry
	head  int // next write position
	count int // total entries written (capped at ringBufferSize)
}

// Global is the shared application-wide logger instance.
var Global *Logger

// Init sets up the global logger. Call once during app startup.
// It creates a log file at <logDir>/sentinel-os-<date>.log and mirrors
// all output to stdout.
func Init(logDir string) error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("logger: cannot create log directory %q: %w", logDir, err)
	}

	today := time.Now().Format("2006-01-02")
	logPath := filepath.Join(logDir, "sentinel-os-"+today+".log")

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("logger: cannot open log file %q: %w", logPath, err)
	}

	// File logger: plain text, no ANSI colors
	fileLog := log.New(f, "", 0)

	Global = &Logger{
		fileLog: fileLog,
		logFile: f,
	}

	Global.write(INFO, "=== Sentinel-OS Logger Initialized ===")
	Global.write(INFO, fmt.Sprintf("Log file: %s", logPath))
	fmt.Printf("%s[SENTINEL-OS]%s Log file: %s%s%s\n", colorInfo, colorReset, colorCaller, logPath, colorReset)

	return nil
}

// Close flushes and closes the underlying log file. Call on app shutdown.
func Close() {
	if Global != nil && Global.logFile != nil {
		Global.write(INFO, "=== Sentinel-OS Logger Closing ===")
		Global.logFile.Close()
	}
}

// write is the internal, thread-safe log writer.
func (l *Logger) write(level Level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	timeStr := now.Format("15:04:05.000")
	fullTime := now.Format("2006-01-02 15:04:05.000")

	// Get caller info (skip 3 frames: write -> public method -> caller)
	_, file, line, ok := runtime.Caller(3)
	caller := "unknown"
	if ok {
		parts := strings.Split(filepath.ToSlash(file), "/")
		if len(parts) >= 2 {
			caller = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		} else {
			caller = file
		}
		caller = fmt.Sprintf("%s:%d", caller, line)
	}

	// ── Write to ring buffer (no allocation, fixed size) ──────────────────────
	entry := LogEntry{
		Time:    timeStr,
		Level:   level.String(),
		Caller:  caller,
		Message: msg,
	}
	l.ring[l.head] = entry
	l.head = (l.head + 1) % ringBufferSize
	if l.count < ringBufferSize {
		l.count++
	}

	// ── Write plain text to file ───────────────────────────────────────────────
	plainEntry := fmt.Sprintf("[%s] [%-5s] (%s) %s", fullTime, level.String(), caller, msg)
	l.fileLog.Println(plainEntry)

	// ── Write color-coded to terminal ──────────────────────────────────────────
	fmt.Printf(
		"%s[%s]%s %s[%-5s]%s %s(%s)%s %s\n",
		colorTime, timeStr, colorReset,
		levelColor(level), level.String(), colorReset,
		colorCaller, caller, colorReset,
		msg,
	)
}

// GetRecentLogs returns up to `limit` most-recent log entries from the ring
// buffer. If limit <= 0 or > ringBufferSize, it returns all stored entries.
// This is safe to call from multiple goroutines.
func GetRecentLogs(limit int) []LogEntry {
	if Global == nil {
		return nil
	}

	Global.mu.Lock()
	defer Global.mu.Unlock()

	total := Global.count
	if limit <= 0 || limit > total {
		limit = total
	}
	if limit == 0 {
		return []LogEntry{}
	}

	out := make([]LogEntry, limit)
	// The oldest entry lives at position: (head - count + ringBufferSize) % ringBufferSize
	// The newest is at: (head - 1 + ringBufferSize) % ringBufferSize
	// We want the last `limit` entries in chronological order.
	startOffset := total - limit // how many entries to skip from the oldest
	for i := 0; i < limit; i++ {
		idx := (Global.head - total + startOffset + i + ringBufferSize*2) % ringBufferSize
		out[i] = Global.ring[idx]
	}
	return out
}

// ── Public logging API ────────────────────────────────────────────────────────

func Debug(format string, args ...any) {
	if Global == nil {
		return
	}
	Global.write(DEBUG, fmt.Sprintf(format, args...))
}

func Info(format string, args ...any) {
	if Global == nil {
		return
	}
	Global.write(INFO, fmt.Sprintf(format, args...))
}

func Warn(format string, args ...any) {
	if Global == nil {
		return
	}
	Global.write(WARN, fmt.Sprintf(format, args...))
}

func Error(format string, args ...any) {
	if Global == nil {
		return
	}
	Global.write(ERROR, fmt.Sprintf(format, args...))
}

func Fatal(format string, args ...any) {
	if Global == nil {
		fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
		os.Exit(1)
	}
	Global.write(FATAL, fmt.Sprintf(format, args...))
	Close()
	os.Exit(1)
}

// Request logs an outgoing HTTP request.
func Request(provider, method, url string) {
	if Global == nil {
		return
	}
	Global.write(DEBUG, fmt.Sprintf("→ HTTP %s [%s] %s", method, provider, url))
}

// Response logs an incoming HTTP response.
func Response(provider string, statusCode int) {
	if Global == nil {
		return
	}
	level := INFO
	if statusCode >= 400 {
		level = ERROR
	}
	Global.write(level, fmt.Sprintf("← HTTP %d [%s]", statusCode, provider))
}

// FileWriter returns an io.Writer that writes to both the log file and stdout.
func FileWriter() io.Writer {
	if Global == nil || Global.logFile == nil {
		return os.Stdout
	}
	return io.MultiWriter(os.Stdout, Global.logFile)
}
