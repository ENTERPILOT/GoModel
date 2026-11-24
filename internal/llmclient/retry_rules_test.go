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

func retryRulesTestClient(t *testing.T, statusQueue []int) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i < len(statusQueue) {
			w.WriteHeader(statusQueue[i])
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 5
	cfg.Retry.InitialBackoff = time.Millisecond
	cfg.Retry.MaxBackoff = time.Millisecond
	cfg.CircuitBreaker.Enabled = false
	return New(cfg, nil), &calls
}

func TestClient_PerStatusRetryRuleOverridesMaxRetries(t *testing.T) {
	client, calls := retryRulesTestClient(t, []int{503, 503, 503, 503, 503, 503})

	ctx := httpclient.WithAttemptRetryRules(context.Background(), []httpclient.AttemptRetryRule{
		{Status: 503, MaxRetries: 1, Delay: time.Millisecond},
	})
	_, err := client.DoRaw(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}})
	require.Error(t, err)
	assert.Equal(t, int32(2), calls.Load(), "a 503 rule with one retry makes exactly two attempts")
}

func TestClient_PerStatusRetryRuleCanDisableRetry(t *testing.T) {
	client, calls := retryRulesTestClient(t, []int{429, 429, 429})

	ctx := httpclient.WithAttemptRetryRules(context.Background(), []httpclient.AttemptRetryRule{
		{Status: 429, MaxRetries: 0},
	})
	_, err := client.DoRaw(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load(), "a rule with zero retries disables retries for its status")
}

func TestClient_PerStatusRetryRuleUsesItsOwnDelay(t *testing.T) {
	client, calls := retryRulesTestClient(t, []int{429, 200})

	const delay = 60 * time.Millisecond
	ctx := httpclient.WithAttemptRetryRules(context.Background(), []httpclient.AttemptRetryRule{
		{Status: 429, MaxRetries: 2, Delay: delay},
	})
	started := time.Now()
	_, err := client.DoRaw(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
	assert.GreaterOrEqual(t, time.Since(started), delay, "the retry waits the rule's fixed delay")
}

func TestClient_StatusWithoutRuleKeepsConfiguredRetry(t *testing.T) {
	client, calls := retryRulesTestClient(t, []int{502, 200})

	ctx := httpclient.WithAttemptRetryRules(context.Background(), []httpclient.AttemptRetryRule{
		{Status: 429, MaxRetries: 0},
	})
	_, err := client.DoRaw(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "a status with no rule keeps the configured retry policy")
}

func TestClient_RetryRulesFromModelResolution(t *testing.T) {
	client, calls := retryRulesTestClient(t, []int{500, 200})

	ctx := core.WithWorkflow(context.Background(), &core.Workflow{
		Resolution: &core.RequestModelResolution{
			RetryRules: core.UpstreamRetryRules{{Status: 500, MaxRetries: 1, Delay: time.Millisecond}},
		},
	})
	_, err := client.DoRaw(ctx, Request{Method: http.MethodPost, Endpoint: "/chat/completions", Body: map[string]any{}})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "rules on the request resolution drive retries")
}
