// Package logx provides the small structured logger used by the daemon.
//
// It intentionally does not depend on log/slog: slog was added to the
// standard library in Go 1.21, while this project also supports the Go 1.20
// toolchain used by the compatibility build targets.
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Level is the minimum severity emitted by a Logger.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Logger writes one JSON object per log entry. Logger is safe for concurrent
// use by the daemon and its HTTP clients.
type Logger struct {
	out io.Writer
	min Level
	mu  sync.Mutex
}

// New constructs a logger which writes to out and emits entries at or above
// min. A nil writer discards entries.
func New(out io.Writer, min Level) *Logger {
	if out == nil {
		out = io.Discard
	}
	return &Logger{out: out, min: min}
}

// Debug logs a debug-level entry.
func (l *Logger) Debug(msg string, args ...interface{}) { l.log(LevelDebug, msg, args...) }

// Info logs an info-level entry.
func (l *Logger) Info(msg string, args ...interface{}) { l.log(LevelInfo, msg, args...) }

// Warn logs a warning-level entry.
func (l *Logger) Warn(msg string, args ...interface{}) { l.log(LevelWarn, msg, args...) }

// Error logs an error-level entry.
func (l *Logger) Error(msg string, args ...interface{}) { l.log(LevelError, msg, args...) }

func (l *Logger) log(level Level, msg string, args ...interface{}) {
	if l == nil || level < l.min {
		return
	}

	record := map[string]interface{}{
		"time":  time.Now().Format(time.RFC3339Nano),
		"level": level.String(),
		"msg":   msg,
	}
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			record["!BADKEY"] = normalize(args[i])
			break
		}
		key, ok := args[i].(string)
		if !ok {
			key = fmt.Sprint(args[i])
		}
		record[key] = normalize(args[i+1])
	}

	data, err := json.Marshal(record)
	if err != nil {
		// Logging must never take the daemon down because a field cannot be
		// encoded. Keep the fallback useful and valid as plain text.
		data = []byte(fmt.Sprintf(`{"time":%q,"level":%q,"msg":%q,"log_error":%q}`,
			time.Now().Format(time.RFC3339Nano), level.String(), msg, err.Error()))
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintln(l.out, string(data))
}

func normalize(value interface{}) interface{} {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	return value
}

func (level Level) String() string {
	switch level {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return fmt.Sprintf("LEVEL(%d)", level)
	}
}
