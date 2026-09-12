package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadedFailoverPolicy(t *testing.T, cfg config.FailoverConfig) *FailoverPolicy {
	t.Helper()
	err := config.LoadFailoverPolicy(&cfg)
	require.NoError(t, err)

	return NewFailoverPolicy(cfg)
}

func TestFailoverPolicyShouldRetry(t *testing.T) {
	custom := loadedFailoverPolicy(t, config.FailoverConfig{
		RetryOnStatuses: []string{"408", "503"},
		RetryOnErrors:   []string{"overloaded", "4xx quota exceeded"},
	})

	tests := []struct {
		name    string
		policy  *FailoverPolicy
		status  int
		code    string
		message string
		want    bool
	}{
		{"nil policy uses defaults: 5xx", nil, http.StatusBadGateway, "", "bad gateway", true},
		{"nil policy uses defaults: plain 400", nil, http.StatusBadRequest, "", "invalid request", false},
		{"default matches error code words", nil, http.StatusBadRequest, "model_not_found", "nope", true},
		{"custom status listed", custom, http.StatusRequestTimeout, "", "slow", true},
		{"custom status replaces default 429", custom, http.StatusTooManyRequests, "", "slow down", false},
		{"custom status replaces default 500", custom, http.StatusInternalServerError, "", "boom", false},
		{"custom phrase", custom, http.StatusBadRequest, "", "Engine Overloaded, retry later", true},
		{"custom phrase replaces default model heuristics", custom, http.StatusBadRequest, "", "model gpt-9 does not exist", false},
		{"status-scoped phrase within class", custom, http.StatusForbidden, "", "quota exceeded for org", true},
		{"status-scoped phrase outside class", custom, http.StatusInternalServerError, "", "quota exceeded for org", false},
		{"all words required", custom, http.StatusForbidden, "", "quota fine", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := core.NewProviderError("openai", tt.status, tt.message, nil)
			if tt.code != "" {
				err = err.WithCode(tt.code)
			}
			got := tt.policy.ShouldRetry(err)
			require.Equal(t, tt.want, got)
		})
	}
	got := custom.ShouldRetry(io.EOF)
	assert.False(t, got)
}

func threeTargetFixture(policy *FailoverPolicy) (*InferenceOrchestrator, *core.Workflow) {
	o, workflow := failoverTestFixture()
	o.failoverPolicy = policy
	o.failoverResolver = stubFailoverResolver{selectors: []core.ModelSelector{
		{Provider: "openai", Model: "a"},
		{Provider: "openai", Model: "b"},
		{Provider: "openai", Model: "c"},
	}}
	return o, workflow
}

func TestTryFailoverResponseHonorsMaxAttempts(t *testing.T) {
	tests := []struct {
		name        string
		maxAttempts int
		wantCalls   []string
	}{
		{"zero sweeps every target", 0, []string{"openai/a", "openai/b", "openai/c"}},
		{"cap stops the sweep", 2, []string{"openai/a", "openai/b"}},
		{"cap above the target count is harmless", 5, []string{"openai/a", "openai/b", "openai/c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, workflow := threeTargetFixture(&FailoverPolicy{MaxAttempts: tt.maxAttempts})
			primaryErr := core.NewProviderError("openai", http.StatusBadGateway, "primary down", nil)
			var calls []string
			call := func(selector core.ModelSelector, _, _ string) (string, string, error) {
				calls = append(calls, selector.QualifiedModel())
				return "", "", core.NewProviderError("openai", http.StatusBadGateway, selector.Model+" down", nil)
			}

			_, meta, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

			require.False(t, meta.UsedFailover)
			require.Error(t, err)
			require.Equal(t, tt.wantCalls, calls)
			// The client sees the last attempted target's error, not the cap itself.
			want := tt.wantCalls[len(tt.wantCalls)-1]
			require.ErrorContains(t, err, strings.TrimPrefix(want, "openai/")+" down", "want the error of %s", want)
		})
	}
}

// Targets skipped before a call (rate-limited routes) do not consume attempts.
func TestTryFailoverResponseMaxAttemptsCountsCallsOnly(t *testing.T) {
	o, workflow := threeTargetFixture(&FailoverPolicy{MaxAttempts: 1})
	o.routeGate = blockingRouteGate{blocked: map[string]bool{"openai/a": true}}
	primaryErr := core.NewProviderError("openai", http.StatusBadGateway, "primary down", nil)
	var calls []string
	call := func(selector core.ModelSelector, _, _ string) (string, string, error) {
		calls = append(calls, selector.QualifiedModel())
		return "ok", "openai", nil
	}

	_, meta, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.True(t, meta.UsedFailover)
	require.NoError(t, err)
	require.Equal(t, "openai/b", meta.FailoverModel)
	require.Len(t, calls, 1)
}

func TestTryFailoverStreamHonorsMaxAttempts(t *testing.T) {
	o, workflow := threeTargetFixture(&FailoverPolicy{MaxAttempts: 1})
	primaryErr := core.NewProviderError("openai", http.StatusBadGateway, "primary down", nil)
	var calls []string
	call := func(selector core.ModelSelector, _, _ string) (io.ReadCloser, string, string, error) {
		calls = append(calls, selector.QualifiedModel())
		return nil, "", "", core.NewProviderError("openai", http.StatusBadGateway, selector.Model+" down", nil)
	}

	stream, _, err := tryFailoverStream(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.Nil(t, stream)
	require.Error(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, "openai/a", calls[0])
}

// A policy that does not list the primary's failure leaves the request alone.
func TestTryFailoverResponseSkipsWhenPolicyDoesNotMatch(t *testing.T) {
	o, workflow := threeTargetFixture(loadedFailoverPolicy(t, config.FailoverConfig{RetryOnStatuses: []string{"503"}}))
	primaryErr := core.NewProviderError("openai", http.StatusTooManyRequests, "slow down", nil)
	called := false
	call := func(core.ModelSelector, string, string) (string, string, error) {
		called = true
		return "ok", "openai", nil
	}

	_, meta, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.False(t, called)
	require.False(t, meta.UsedFailover)
	require.Equal(t, primaryErr, err)
}

// The stream path applies the same gate: a primary failure the policy does
// not list is returned untouched, without calling any target.
func TestTryFailoverStreamSkipsWhenPolicyDoesNotMatch(t *testing.T) {
	o, workflow := threeTargetFixture(loadedFailoverPolicy(t, config.FailoverConfig{RetryOnStatuses: []string{"503"}}))
	primaryErr := core.NewProviderError("openai", http.StatusTooManyRequests, "slow down", nil)
	called := false
	call := func(core.ModelSelector, string, string) (io.ReadCloser, string, string, error) {
		called = true
		return nil, "", "", nil
	}

	stream, _, err := tryFailoverStream(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.False(t, called)
	require.Nil(t, stream)
	require.Equal(t, primaryErr, err)
}
