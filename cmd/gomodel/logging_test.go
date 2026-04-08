package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
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
			if err != nil {
				t.Fatalf("parseLogLevel(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseLogLevelInvalid(t *testing.T) {
	t.Parallel()

	if _, err := parseLogLevel("trace"); err == nil {
		t.Fatal("parseLogLevel(trace) should fail")
	}
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
			if handler.Enabled(ctx, slog.LevelInfo) {
				t.Fatal("handler.Enabled(info) = true, want false")
			}
			if !handler.Enabled(ctx, slog.LevelWarn) {
				t.Fatal("handler.Enabled(warn) = false, want true")
			}
			if !handler.Enabled(ctx, slog.LevelError) {
				t.Fatal("handler.Enabled(error) = false, want true")
			}
		})
	}
}
