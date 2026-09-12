package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// stubFailoverResolver returns a fixed selector list regardless of input.
type stubFailoverResolver struct {
	selectors []core.ModelSelector
}

func (s stubFailoverResolver) ResolveFailovers(_ *core.RequestModelResolution, _ core.Operation) []core.ModelSelector {
	return s.selectors
}

func failoverTestFixture() (*InferenceOrchestrator, *core.Workflow) {
	o := &InferenceOrchestrator{
		failoverResolver: stubFailoverResolver{
			selectors: []core.ModelSelector{{Provider: "openai", Model: "gpt-5"}},
		},
	}
	workflow := &core.Workflow{
		Endpoint:   core.EndpointDescriptor{Operation: core.OperationChatCompletions},
		Resolution: &core.RequestModelResolution{},
	}
	return o, workflow
}

// A canceled context means the client is gone; failover must not sweep providers
// (doing so wastes attempts and trips healthy providers' circuit breakers).
func TestTryFailoverResponseSkipsWhenContextCanceled(t *testing.T) {
	o, workflow := failoverTestFixture()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primaryErr := core.NewProviderError("openai", http.StatusBadGateway, "context canceled", context.Canceled)
	called := false
	call := func(core.ModelSelector, string, string) (string, string, error) {
		called = true
		return "", "", core.NewProviderError("openai", http.StatusBadGateway, "unexpected failover call", nil)
	}

	_, meta, err := tryFailoverResponse(ctx, o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.False(t, called)
	require.False(t, meta.UsedFailover)
	require.ErrorIs(t, err, context.Canceled)
}

// The guard is scoped to a done context: a live request still attempts failover.
func TestTryFailoverResponseAttemptsWhenContextLive(t *testing.T) {
	o, workflow := failoverTestFixture()

	primaryErr := core.NewProviderError("openai", http.StatusInternalServerError, "primary boom", nil)
	called := false
	call := func(core.ModelSelector, string, string) (string, string, error) {
		called = true
		return "ok", "openai", nil
	}

	resp, meta, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.True(t, called)
	require.True(t, meta.UsedFailover)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
}

// blockingRouteGate refuses the listed qualified models.
type blockingRouteGate struct {
	blocked map[string]bool
}

func (g blockingRouteGate) RouteAvailable(_, model string) bool {
	return !g.blocked[model]
}

// A failover target whose provider or model is rate-saturated is skipped, so
// the sweep moves on to the next candidate instead of burning its attempt.
func TestTryFailoverResponseSkipsRateLimitedTargets(t *testing.T) {
	o, workflow := failoverTestFixture()
	o.failoverResolver = stubFailoverResolver{selectors: []core.ModelSelector{
		{Provider: "openai", Model: "gpt-5"},
		{Provider: "anthropic", Model: "claude"},
	}}
	o.routeGate = blockingRouteGate{blocked: map[string]bool{"openai/gpt-5": true}}

	primaryErr := core.NewProviderError("openai", http.StatusInternalServerError, "primary boom", nil)
	var attempted []string
	call := func(selector core.ModelSelector, _, _ string) (string, string, error) {
		attempted = append(attempted, selector.QualifiedModel())
		return "ok", "anthropic", nil
	}

	resp, meta, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.Len(t, attempted, 1)
	require.Equal(t, "anthropic/claude", attempted[0])
	require.True(t, meta.UsedFailover)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.Equal(t, "anthropic/claude", meta.FailoverModel)
}

// The stream sweep shares the route-gate skip with the response sweep.
func TestTryFailoverStreamSkipsRateLimitedTargets(t *testing.T) {
	o, workflow := failoverTestFixture()
	o.failoverResolver = stubFailoverResolver{selectors: []core.ModelSelector{
		{Provider: "openai", Model: "gpt-5"},
		{Provider: "anthropic", Model: "claude"},
	}}
	o.routeGate = blockingRouteGate{blocked: map[string]bool{"openai/gpt-5": true}}

	primaryErr := core.NewProviderError("openai", http.StatusInternalServerError, "primary boom", nil)
	var attempted []string
	call := func(selector core.ModelSelector, _, _ string) (io.ReadCloser, string, string, error) {
		attempted = append(attempted, selector.QualifiedModel())
		return io.NopCloser(strings.NewReader("data")), "anthropic", "claude", nil
	}

	stream, meta, err := tryFailoverStream(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.Len(t, attempted, 1)
	require.Equal(t, "anthropic/claude", attempted[0])
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.Equal(t, "anthropic/claude", meta.FailoverModel)

	stream.Close()
}

func TestTryFailoverStreamSkipsWhenContextCanceled(t *testing.T) {
	o, workflow := failoverTestFixture()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primaryErr := core.NewProviderError("openai", http.StatusBadGateway, "context canceled", context.Canceled)
	called := false
	call := func(core.ModelSelector, string, string) (io.ReadCloser, string, string, error) {
		called = true
		return nil, "", "", core.NewProviderError("openai", http.StatusBadGateway, "unexpected failover call", nil)
	}

	stream, _, err := tryFailoverStream(ctx, o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	require.False(t, called)
	require.Nil(t, stream)
	require.ErrorIs(t, err, context.Canceled)
}

func TestShouldAttemptFailover(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		want    bool
	}{
		// Server-side and rate-limit failures always fall back.
		{"500 server error", http.StatusInternalServerError, "internal error", true},
		{"429 rate limited", http.StatusTooManyRequests, "slow down", true},

		// Model-availability phrasing falls back regardless of status code.
		{"model not found message", http.StatusBadRequest, "model gpt-9 does not exist", true},

		// 404 with availability phrasing (no literal "model") still falls back:
		// providers report retired/unavailable models this way.
		{"availability 404", http.StatusNotFound, "Claude Fable 5 is not available. Please use Opus 4.8.", true},
		{"deprecated 404", http.StatusNotFound, "this checkpoint is deprecated", true},

		// 404s without availability phrasing must NOT fall back — they are
		// genuine routing/endpoint misses, not model failures.
		{"generic endpoint 404", http.StatusNotFound, "endpoint not found", false},
		{"route 404", http.StatusNotFound, "404 page not found", false},
		{"unknown path 404", http.StatusNotFound, "no route for /v1/foo", false},

		// Aggregator providers relay transient failures of their own upstream
		// as 4xx client errors; those must fall back (issue #605).
		{"opencode upstream 400", http.StatusBadRequest, "Error from provider (Console Go): Upstream request failed", true},
		{"upstream timeout 400", http.StatusBadRequest, "upstream timed out", true},
		{"upstream unavailable 400", http.StatusBadRequest, "upstream provider is currently unavailable", true},

		// Mentioning an upstream without failure phrasing is a genuine
		// validation error and must NOT fall back.
		{"upstream capability 400", http.StatusBadRequest, "parameter tools is not supported by the upstream provider", false},

		// A plain client error without availability phrasing is not retried.
		{"plain 400", http.StatusBadRequest, "invalid request", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := core.NewProviderError("anthropic", tt.status, tt.message, nil)
			got := ShouldAttemptFailover(err)
			require.Equal(t, tt.want, got)
		})
	}
}

// A saturated primary route must never reach the provider (the upstream would
// serve it and defeat the gateway's limit); its stored 429 seeds the sweep.
func TestExecuteTranslatedSkipsSaturatedPrimaryAndFailsOver(t *testing.T) {
	o, workflow := failoverTestFixture()
	saturated := core.NewRateLimitError("ratelimit", "rate limit exceeded for provider openai").WithCode("rate_limit_exceeded")
	ctx := core.WithPrimaryRouteSaturated(context.Background(), saturated)

	var calls []string
	resp, meta, err := executeTranslatedWithFailover(
		ctx, o, workflow, "req", "openai/gpt-4o", "openai",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, req string) (string, string, error) {
			calls = append(calls, req)
			require.NotEqual(t, "req", req)

			return "ok", "openai", nil
		},
	)

	require.Len(t, calls, 1)
	require.Equal(t, "openai/gpt-5", calls[0])
	require.True(t, meta.UsedFailover)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.Equal(t, "openai/gpt-5", meta.FailoverModel)
}

// When every failover target is also unavailable, the client receives the
// original rate-limit rejection, not a provider error.
func TestExecuteTranslatedSaturatedPrimarySurfaces429WhenNoTargetRemains(t *testing.T) {
	o, workflow := failoverTestFixture()
	o.routeGate = blockingRouteGate{blocked: map[string]bool{"openai/gpt-5": true}}
	saturated := core.NewRateLimitError("ratelimit", "rate limit exceeded for provider openai").WithCode("rate_limit_exceeded")
	ctx := core.WithPrimaryRouteSaturated(context.Background(), saturated)

	_, meta, err := executeTranslatedWithFailover(
		ctx, o, workflow, "req", "openai/gpt-4o", "openai",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, _ string) (string, string, error) {
			t.Fatal("no provider call expected: primary saturated, failover gated")
			return "", "", nil
		},
	)

	require.False(t, meta.UsedFailover)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusTooManyRequests, gatewayErr.HTTPStatusCode())
}

// The stream path shares the skip.
func TestStreamTranslatedSkipsSaturatedPrimaryAndFailsOver(t *testing.T) {
	o, workflow := failoverTestFixture()
	saturated := core.NewRateLimitError("ratelimit", "rate limit exceeded for model gpt-4o").WithCode("rate_limit_exceeded")
	ctx := core.WithPrimaryRouteSaturated(context.Background(), saturated)

	var calls []string
	stream, meta, err := streamTranslatedProviderRequest(
		o, ctx, workflow, "req", "openai/gpt-4o", "openai",
		"openai", "openai", "gpt-4o",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, req string) (io.ReadCloser, error) {
			calls = append(calls, req)
			require.NotEqual(t, "req", req)

			return io.NopCloser(strings.NewReader("data")), nil
		},
	)

	require.Len(t, calls, 1)
	require.Equal(t, "openai/gpt-5", calls[0])
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.True(t, meta.UsedFailover)
	require.Equal(t, "openai/gpt-5", meta.FailoverModel)

	stream.Close()
}
