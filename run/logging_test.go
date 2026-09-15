package run

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  slog.Level
	}{
		{name: "default info", input: "", want: slog.LevelInfo},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "info alias", input: "inf", want: slog.LevelInfo},
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "debug alias", input: "dbg", want: slog.LevelDebug},
		{name: "warn", input: "warn", want: slog.LevelWarn},
		{name: "warning alias", input: "warning", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "error alias", input: "err", want: slog.LevelError},
		{name: "trimmed", input: "  WARN  ", want: slog.LevelWarn},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseLogLevel(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got, "parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
		})
	}
}

func TestNewLogHandlerFormatSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		isTTY    bool
		format   string
		wantJSON bool
	}{
		{name: "unset auto-detects json without tty", isTTY: false, format: "", wantJSON: true},
		{name: "unset auto-detects text on tty", isTTY: true, format: "", wantJSON: false},
		{name: "explicit json", isTTY: false, format: "json", wantJSON: true},
		{name: "explicit json on tty", isTTY: true, format: "json", wantJSON: true},
		{name: "explicit text without tty", isTTY: false, format: "text", wantJSON: false},
		{name: "json trimmed and case-insensitive", isTTY: false, format: " JSON ", wantJSON: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := newLogHandler(io.Discard, tt.isTTY, tt.format, slog.LevelInfo)
			_, gotJSON := handler.(*slog.JSONHandler)
			require.Equal(t, tt.wantJSON, gotJSON, "newLogHandler(isTTY=%v, format=%q) json = %v, want %v", tt.isTTY, tt.format, gotJSON, tt.wantJSON)
		})
	}
}

func TestParseLogLevelInvalid(t *testing.T) {
	t.Parallel()
	_, err := parseLogLevel("trace")
	require.Error(t, err)
}

func TestNewLogHandlerUsesConfiguredLevel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tests := []struct {
		name   string
		isTTY  bool
		format string
	}{
		{name: "json handler", isTTY: false, format: "json"},
		{name: "text handler", isTTY: true, format: "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := newLogHandler(io.Discard, tt.isTTY, tt.format, slog.LevelWarn)
			require.False(t, handler.Enabled(ctx, slog.LevelInfo))
			require.True(t, handler.Enabled(ctx, slog.LevelWarn))
			require.True(t, handler.Enabled(ctx, slog.LevelError))
		})
	}
}

// Request-derived values (request IDs, paths, model names, error text) are
// logged as slog attributes. Both handlers must quote control characters so a
// caller cannot forge log lines or terminal escapes through them.
func TestNewLogHandlerEscapesAttrValues(t *testing.T) {
	t.Parallel()

	const hostile = "a\nINFO forged\x1b[31m"

	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := slog.New(newLogHandler(&buf, false, format, slog.LevelInfo))
			logger.Info("request failed", "request_id", hostile)

			out := buf.String()
			require.Equal(t, 1, strings.Count(out, "\n"))
			require.NotContains(t, out, "\x1b", "%s handler leaked control characters: %q", format, out)
		})
	}
}
