package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"gomodel/internal/core"
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

	_, _, _, _, didFailover, err := tryFailoverResponse(ctx, o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	if called {
		t.Fatal("tryFailoverResponse invoked a failover provider on a canceled context; it must short-circuit")
	}
	if didFailover {
		t.Fatalf("didFailover = true, want false when context is canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the primary error wrapping context.Canceled", err)
	}
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

	resp, _, _, _, didFailover, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	if !called {
		t.Fatal("tryFailoverResponse did not attempt failover on a live context")
	}
	if !didFailover || err != nil || resp != "ok" {
		t.Fatalf("failover result = (resp:%q didFailover:%v err:%v), want (ok true <nil>)", resp, didFailover, err)
	}
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

	resp, _, _, failoverModel, didFailover, err := tryFailoverResponse(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	if len(attempted) != 1 || attempted[0] != "anthropic/claude" {
		t.Fatalf("attempted = %v, want only anthropic/claude (rate-limited target skipped)", attempted)
	}
	if !didFailover || err != nil || resp != "ok" || failoverModel != "anthropic/claude" {
		t.Fatalf("failover result = (resp:%q model:%q didFailover:%v err:%v), want anthropic success", resp, failoverModel, didFailover, err)
	}
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

	stream, _, _, _, failoverModel, err := tryFailoverStream(context.Background(), o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	if len(attempted) != 1 || attempted[0] != "anthropic/claude" {
		t.Fatalf("attempted = %v, want only anthropic/claude (rate-limited target skipped)", attempted)
	}
	if err != nil || stream == nil || failoverModel != "anthropic/claude" {
		t.Fatalf("failover result = (stream:%v model:%q err:%v), want anthropic success", stream != nil, failoverModel, err)
	}
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

	stream, _, _, _, _, err := tryFailoverStream(ctx, o, workflow, "openai/gpt-4o", "openai", primaryErr, call)

	if called {
		t.Fatal("tryFailoverStream invoked a failover provider on a canceled context; it must short-circuit")
	}
	if stream != nil {
		t.Fatal("tryFailoverStream returned a stream on a canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the primary error wrapping context.Canceled", err)
	}
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

		// A plain client error without availability phrasing is not retried.
		{"plain 400", http.StatusBadRequest, "invalid request", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := core.NewProviderError("anthropic", tt.status, tt.message, nil)
			if got := ShouldAttemptFailover(err); got != tt.want {
				t.Fatalf("ShouldAttemptFailover(%d, %q) = %v, want %v", tt.status, tt.message, got, tt.want)
			}
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
	resp, _, _, failoverModel, didFailover, err := executeTranslatedWithFailover(
		ctx, o, workflow, "req", "openai/gpt-4o", "openai",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, req string) (string, string, error) {
			calls = append(calls, req)
			if req == "req" {
				t.Fatal("saturated primary route reached the provider")
			}
			return "ok", "openai", nil
		},
	)

	if len(calls) != 1 || calls[0] != "openai/gpt-5" {
		t.Fatalf("provider calls = %v, want only the failover target", calls)
	}
	if !didFailover || err != nil || resp != "ok" || failoverModel != "openai/gpt-5" {
		t.Fatalf("result = (resp:%q model:%q didFailover:%v err:%v), want failover success", resp, failoverModel, didFailover, err)
	}
}

// When every failover target is also unavailable, the client receives the
// original rate-limit rejection, not a provider error.
func TestExecuteTranslatedSaturatedPrimarySurfaces429WhenNoTargetRemains(t *testing.T) {
	o, workflow := failoverTestFixture()
	o.routeGate = blockingRouteGate{blocked: map[string]bool{"openai/gpt-5": true}}
	saturated := core.NewRateLimitError("ratelimit", "rate limit exceeded for provider openai").WithCode("rate_limit_exceeded")
	ctx := core.WithPrimaryRouteSaturated(context.Background(), saturated)

	_, _, _, _, didFailover, err := executeTranslatedWithFailover(
		ctx, o, workflow, "req", "openai/gpt-4o", "openai",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, _ string) (string, string, error) {
			t.Fatal("no provider call expected: primary saturated, failover gated")
			return "", "", nil
		},
	)

	if didFailover {
		t.Fatal("didFailover = true, want false")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want the original 429", err)
	}
}

// The stream path shares the skip.
func TestStreamTranslatedSkipsSaturatedPrimaryAndFailsOver(t *testing.T) {
	o, workflow := failoverTestFixture()
	saturated := core.NewRateLimitError("ratelimit", "rate limit exceeded for model gpt-4o").WithCode("rate_limit_exceeded")
	ctx := core.WithPrimaryRouteSaturated(context.Background(), saturated)

	var calls []string
	stream, _, _, _, failoverModel, usedFailover, err := streamTranslatedProviderRequest(
		o, ctx, workflow, "req", "openai/gpt-4o", "openai",
		"openai", "openai", "gpt-4o",
		func(req string, selector core.ModelSelector) string { return selector.QualifiedModel() },
		func(_ context.Context, req string) (io.ReadCloser, error) {
			calls = append(calls, req)
			if req == "req" {
				t.Fatal("saturated primary route reached the provider")
			}
			return io.NopCloser(strings.NewReader("data")), nil
		},
	)

	if len(calls) != 1 || calls[0] != "openai/gpt-5" {
		t.Fatalf("provider calls = %v, want only the failover target", calls)
	}
	if err != nil || stream == nil || !usedFailover || failoverModel != "openai/gpt-5" {
		t.Fatalf("result = (stream:%v model:%q usedFailover:%v err:%v), want failover success", stream != nil, failoverModel, usedFailover, err)
	}
	stream.Close()
}
