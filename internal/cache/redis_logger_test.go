package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestSlogRedisLogger_Printf_RoutesThroughSlogAtWarn(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	slogRedisLogger{}.Printf(context.Background(), "connection pool: failed after %d attempts", 5)

	var entry struct {
		Level   string `json:"level"`
		Message string `json:"msg"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("failed to decode log entry %q: %v", buf.String(), err)
	}
	if entry.Level != "WARN" {
		t.Errorf("level = %q, want WARN", entry.Level)
	}
	if want := "connection pool: failed after 5 attempts"; entry.Message != want {
		t.Errorf("msg = %q, want %q", entry.Message, want)
	}
}
