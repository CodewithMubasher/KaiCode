package logging

import (
	"log/slog"
	"os"
	"path/filepath"
)

// maxLogSize is when logs.txt is moved aside at startup (to logs.old.txt), so
// the file stays small enough to send along with a bug report.
const maxLogSize = 5 << 20

// Setup writes logs to file (normally ~/.kai/logs.txt) and makes the result
// the default slog logger. Both Kai and the Chrome extension log here.
func Setup(file string, level string) (*slog.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(file); err == nil && fi.Size() > maxLogSize {
		old := file[:len(file)-len(filepath.Ext(file))] + ".old" + filepath.Ext(file)
		_ = os.Remove(old)
		_ = os.Rename(file, old)
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(logger)
	return logger, nil
}
