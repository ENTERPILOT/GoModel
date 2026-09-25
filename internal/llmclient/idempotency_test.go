package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// keyRecordingServer answers failures first responses with 503, then 200, and
// records the Idempotency-Key of every request it receives.
func keyRecordingServer(t *testing.T, failures int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		attempt := len(keys)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if attempt <= failures {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

// snapshotContext carries a client Idempotency-Key the way ingress captures it.
func snapshotContext(key string) context.Context {
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil,
		map[string][]string{"Idempotency-Key": {key}}, "application/json", nil, false, "req-id", nil)
	return core.WithRequestSnapshot(context.Background(), snapshot)
}

func TestClient_ForwardsIdempotencyKey(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		headers http.Header
		want    string
	}{
		{name: "no key", ctx: context.Background()},
		{name: "client key", ctx: core.WithIdempotencyKey(context.Background(), "req-123"), want: "req-123"},
		{name: "client header from the request snapshot", ctx: snapshotContext("req-456"), want: "req-456"},
		{name: "failover clear overrides the client header", ctx: core.WithIdempotencyKey(snapshotContext("req-456"), "")},
		{name: "cleared for a failover attempt", ctx: core.WithIdempotencyKey(core.WithIdempotencyKey(context.Background(), "req-123"), "")},
		{
			name:    "explicit header wins",
			ctx:     core.WithIdempotencyKey(context.Background(), "req-123"),
			headers: http.Header{"Idempotency-Key": {"passthrough-key"}},
			want:    "passthrough-key",
		},
		{
			name: "guardrail call does not inherit it",
			ctx:  core.WithRequestOrigin(core.WithIdempotencyKey(context.Background(), "req-123"), core.RequestOriginGuardrail),
		},
		{
			name: "plugin call does not inherit it",
			ctx:  core.WithRequestOrigin(core.WithIdempotencyKey(context.Background(), "req-123"), core.RequestOriginPlugin),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, keys := keyRecordingServer(t, 0)
			client := New(DefaultConfig("test", server.URL), nil)

			err := client.Do(tt.ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}, Headers: tt.headers}, nil)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, keys())
		})
	}
}

func TestClient_RetriesReuseIdempotencyKey(t *testing.T) {
	server, keys := keyRecordingServer(t, 2)
	config := DefaultConfig("test", server.URL)
	config.Retry.InitialBackoff = time.Millisecond
	config.Retry.MaxBackoff = time.Millisecond
	client := New(config, nil)

	ctx := core.WithIdempotencyKey(context.Background(), "req-123")
	err := client.Do(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"req-123", "req-123", "req-123"}, keys())
}
