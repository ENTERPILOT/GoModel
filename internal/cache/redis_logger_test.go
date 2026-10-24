package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry)
	require.NoError(t, err, "failed to decode log entry %q: %v", buf.String(), err)
	assert.Equal(t, "WARN", entry.Level)
	want := "connection pool: failed after 5 attempts"
	assert.Equal(t, want, entry.Message)
}
