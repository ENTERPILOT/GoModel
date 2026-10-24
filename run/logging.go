package run

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"golang.org/x/term"
)

const (
	envLogFormat = "LOG_FORMAT"
	envLogLevel  = "LOG_LEVEL"
)

// configureLogging installs the process default logger for LOG_FORMAT and
// LOG_LEVEL, wrapped by the extensions' log handler wrappers. It is the only
// place core builds a slog handler, so no logger core creates escapes them.
func configureLogging(w io.Writer, wrappers []func(slog.Handler) slog.Handler) error {
	level, err := parseLogLevel(os.Getenv(envLogLevel))
	if err != nil {
		return err
	}

	handler, err := wrapLogHandler(newLogHandler(w, detectTTY(w), os.Getenv(envLogFormat), level), wrappers)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(handler))
	return nil
}

// wrapLogHandler applies wrappers in registration order: the first wraps
// handler and each later one wraps the result.
func wrapLogHandler(handler slog.Handler, wrappers []func(slog.Handler) slog.Handler) (slog.Handler, error) {
	for i, wrap := range wrappers {
		wrapped := wrap(handler)
		if wrapped == nil {
			return nil, fmt.Errorf("log handler wrapper %d returned a nil handler", i)
		}
		handler = wrapped
	}
	return handler, nil
}

func detectTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
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
