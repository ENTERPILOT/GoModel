package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

func workflowWithTimeouts(timeouts core.UpstreamTimeouts) context.Context {
	return core.WithWorkflow(context.Background(), &core.Workflow{
		Resolution: &core.RequestModelResolution{Timeouts: timeouts},
	})
}

// delayHeaders holds the response for d, or until the client gives up.
func delayHeaders(r *http.Request, d time.Duration) {
	select {
	case <-time.After(d):
	case <-r.Context().Done():
	}
}

func newTimeoutTestClient(t *testing.T, serverURL string, timeout time.Duration) *Client {
	t.Helper()
	httpCfg := httpclient.DefaultConfig()
	httpCfg.Timeout = timeout
	cfg := DefaultConfig("test", serverURL)
	cfg.Retry.InitialBackoff = time.Millisecond
	cfg.Retry.MaxBackoff = time.Millisecond
	return NewWithHTTPClient(httpclient.NewHTTPClient(&httpCfg), cfg, nil)
}

func TestClient_AppliesResolvedUpstreamTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delayHeaders(r, 150*time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		name    string
		client  time.Duration
		ctx     context.Context
		wantErr bool
	}{
		{name: "gateway-wide timeout without a workflow", client: 50 * time.Millisecond, ctx: context.Background(), wantErr: true},
		{name: "longer model timeout", client: 50 * time.Millisecond, ctx: workflowWithTimeouts(core.UpstreamTimeouts{Timeout: 2 * time.Second})},
		{name: "shorter model timeout", client: 2 * time.Second, ctx: workflowWithTimeouts(core.UpstreamTimeouts{Timeout: 50 * time.Millisecond}), wantErr: true},
		{name: "shorter model response header timeout", client: 2 * time.Second, ctx: workflowWithTimeouts(core.UpstreamTimeouts{ResponseHeaderTimeout: 50 * time.Millisecond}), wantErr: true},
		{
			name:   "explicit context overrides win over the workflow",
			client: 50 * time.Millisecond,
			ctx: httpclient.WithAttemptTimeouts(
				workflowWithTimeouts(core.UpstreamTimeouts{Timeout: 50 * time.Millisecond}),
				httpclient.AttemptTimeouts{Timeout: 2 * time.Second},
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTimeoutTestClient(t, server.URL, tt.client)
			err := client.Do(tt.ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}}, nil)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClient_RetryGetsFreshTimeoutBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delayHeaders(r, 150*time.Millisecond)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	client := newTimeoutTestClient(t, server.URL, 2*time.Second)
	// Two attempts take ~300ms in total, more than one 250ms budget; each
	// fits its own with ~100ms to spare.
	ctx := workflowWithTimeouts(core.UpstreamTimeouts{Timeout: 250 * time.Millisecond})
	err := client.Do(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}}, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}
