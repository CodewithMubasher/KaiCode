package logging

import (
	"log/slog"
	"os"
	"path/filepath"
)

func Setup(file string, level string) (*slog.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: lvl})
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger, nil
}
