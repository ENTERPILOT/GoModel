package run

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
)

const (
	envLogFormat = "LOG_FORMAT"
	envLogLevel  = "LOG_LEVEL"
)

func configureLogging(w io.Writer) error {
	level, err := parseLogLevel(os.Getenv(envLogLevel))
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(newLogHandler(w, detectTTY(w), os.Getenv(envLogFormat), level)))
	return nil
}

// detectTTY reports whether w is a character device, which is what decides
// between human-readable and JSON logs. It treats every character device as a
// terminal — writing logs to /dev/null gets the coloured handler — because the
// only thing that rides on it is formatting.
func detectTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func newLogHandler(w io.Writer, isTTY bool, format string, level slog.Level) slog.Handler {
	format = strings.ToLower(strings.TrimSpace(format))
	if (isTTY && format != "json") || format == "text" {
		return tint.NewTextHandler(w, &tint.Options{
			Level:      level,
			TimeFormat: time.Kitchen,
			NoColor:    !isTTY,
		})
	}
	return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info", "inf":
		return slog.LevelInfo, nil
	case "debug", "dbg":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid %s %q: supported values are debug, info, warn, error", envLogLevel, raw)
	}
}
