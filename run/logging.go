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

	"github.com/enterpilot/gomodel/internal/envcompat"
)

const (
	envLogFormat = "LOG_FORMAT"
	envLogLevel  = "LOG_LEVEL"
)

func configureLogging(w io.Writer) error {
	// Resolve both variables without warning: nothing may log before the
	// handler below is installed, or the deprecation warnings would go to
	// Go's bootstrap text handler on os.Stderr (unparseable in a JSON
	// deployment, wrong writer for embedded callers).
	level, err := parseLogLevel(envcompat.Quiet(envLogLevel))
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(newLogHandler(w, detectTTY(w), envcompat.Quiet(envLogFormat), level)))

	// Re-read through the warning path now that the configured handler is
	// live; Quiet did not consume the warn-once budget, so legacy spellings
	// warn here, exactly once, in the configured format.
	envcompat.Get(envLogLevel)
	envcompat.Get(envLogFormat)
	return nil
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
