package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
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
