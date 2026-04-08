package main

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

func configureLogging(w io.Writer, isTTY bool) error {
	level, err := parseLogLevel(os.Getenv(envLogLevel))
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(newLogHandler(w, isTTY, os.Getenv(envLogFormat), level)))
	return nil
}

func newLogHandler(w io.Writer, isTTY bool, format string, level slog.Level) slog.Handler {
	format = strings.ToLower(strings.TrimSpace(format))
	if (isTTY && format != "json") || format == "text" {
		return tint.NewHandler(w, &tint.Options{
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
