package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

var logger *slog.Logger

func Init(level slog.Level) {
	logger = slog.New(&prettyHandler{w: os.Stderr, level: level})
	slog.SetDefault(logger)
}

func L() *slog.Logger {
	if logger == nil {
		Init(slog.LevelInfo)
	}
	return logger
}

func Infof(format string, args ...any)  { L().Info(fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any)  { L().Warn(fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any) { L().Error(fmt.Sprintf(format, args...)) }
func Debugf(format string, args ...any) { L().Debug(fmt.Sprintf(format, args...)) }
func Plainf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

// ParseLogLevel converts a string level name to slog.Level.
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// prettyHandler outputs: [2026-02-21 5:10:39 PM PST] message
type prettyHandler struct {
	w     io.Writer
	level slog.Level
	mu    sync.Mutex
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format("2006-01-02 3:04:05 PM MST")

	var prefix string
	switch {
	case r.Level >= slog.LevelError:
		prefix = "ERROR: "
	case r.Level >= slog.LevelWarn:
		prefix = "WARN: "
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintf(h.w, "[%s] %s%s\n", ts, prefix, r.Message)
	return err
}

func (h *prettyHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *prettyHandler) WithGroup(_ string) slog.Handler       { return h }
