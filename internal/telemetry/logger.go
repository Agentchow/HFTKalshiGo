package telemetry

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

var logger *slog.Logger

func Init(level slog.Level) {
	opts := &slog.HandlerOptions{Level: level}
	logger = slog.New(slog.NewTextHandler(os.Stderr, opts))
	slog.SetDefault(logger)
}

func L() *slog.Logger {
	if logger == nil {
		Init(slog.LevelInfo)
	}
	return logger
}

func Timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

func Infof(format string, args ...any) {
	L().Info(fmt.Sprintf(format, args...))
}

func Warnf(format string, args ...any) {
	L().Warn(fmt.Sprintf(format, args...))
}

func Errorf(format string, args ...any) {
	L().Error(fmt.Sprintf(format, args...))
}
