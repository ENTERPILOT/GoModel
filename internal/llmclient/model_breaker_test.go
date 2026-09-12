package llmclient

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestModelBreakerStorageBounded(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	// A busy breaker and an open breaker must survive churn.
	busy := client.breakerForModel("busy")
	open := client.breakerForModel("open")
	for range cfg.CircuitBreaker.FailureThreshold {
		open.RecordFailure()
	}
	client.releaseModelBreaker("open", open)
	for i := range maxModelBreakers * 2 {
		model := fmt.Sprintf("caller-model-%d", i)
		breaker := client.breakerForModel(model)
		require.NotNil(t, breaker)

		client.releaseModelBreaker(model, breaker)
	}
	require.LessOrEqual(t, len(client.modelBreakers), maxModelBreakers)
	require.Equal(t, client.breakerForModel("busy"), busy)
	require.Equal(t, client.breakerForModel("open"), open)

	// Expired idle state is removed on the next new model lookup.
	key := sha256.Sum256([]byte(fmt.Sprintf("caller-model-%d", maxModelBreakers*2-1)))
	client.modelBreakers[key].lastUsed = time.Now().Add(-modelBreakerIdleTTL - time.Second)
	client.breakerForModel("new")
	require.Nil(t, client.modelBreakers[key])
}

func TestModelBreakerCapacityRejectsWithoutBypassingProtection(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	for i := range maxModelBreakers {
		client.breakerForModel(fmt.Sprint(i))
	}
	_, err := client.DoRaw(t.Context(), Request{Model: "overflow", Method: "GET", Endpoint: "/test"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "capacity exhausted")
	require.Contains(t, err.Error(), "provider test")
	require.Len(t, client.modelBreakers, maxModelBreakers)
}

func TestFailAfterRetriesIncludesProviderName(t *testing.T) {
	client := New(DefaultConfig("test", ""), nil)
	err := client.failAfterRetries(requestScope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "request failed after retries for provider test")
}

func TestUnknownModelUsesProviderBreaker(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	scope, err := client.beginRequest(t.Context(), Request{}, false)
	require.NoError(t, err)
	require.Equal(t, client.circuitBreaker, scope.breaker)
	require.Empty(t, client.modelBreakers)

	client.finishRequest(scope, 200, nil)
}

func TestInvalidProgrammaticPolicyNeverReachesUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid configuration reached upstream") }))
	defer server.Close()
	for _, field := range []string{"retry", "breaker", "scope"} {
		t.Run(field, func(t *testing.T) {
			cfg := DefaultConfig("test", server.URL)
			switch field {
			case "retry":
				cfg.Retry.RetryOnStatuses = []string{"oops"}
			case "breaker":
				cfg.CircuitBreaker.FailureOnStatuses = []string{"oops"}
			case "scope":
				cfg.CircuitBreaker.Scope = "oops"
			}
			_, err := New(cfg, nil).DoRaw(t.Context(), Request{Method: "GET", Endpoint: "/test"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid resilience configuration")
		})
	}
}

func TestModelBreakerHalfOpenRecoveryIsPerModel(t *testing.T) {
	failing := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing && r.URL.Path == "/model1" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 0
	cfg.CircuitBreaker.Scope = "model"
	cfg.CircuitBreaker.FailureThreshold = 1
	cfg.CircuitBreaker.SuccessThreshold = 1
	client := New(cfg, nil)
	request := func(model string) error {
		return client.Do(t.Context(), Request{Method: "GET", Endpoint: "/" + model, Model: model}, nil)
	}

	require.Error(t, request("model1"))
	err := request("model1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "circuit breaker is open")
	err = // A healthy sibling model keeps serving traffic on its own breaker.
		request("model2")
	require.NoError(t, err)

	failing = false
	breaker := client.breakerForModel("model1")
	client.releaseModelBreaker("model1", breaker)
	breaker.mu.Lock()
	breaker.lastFailure = time.Now().Add(-cfg.CircuitBreaker.Timeout - time.Second)
	breaker.mu.Unlock()
	err = request("model1")
	require.NoError(t, err)
	got := breaker.State()
	require.Equal(t, "closed", got)
	model2 := client.breakerForModel("model2")
	require.NotSame(t, breaker, model2)
	require.Equal(t, "closed", model2.State())
}

func TestModelBreakerKeyedByRequestBodyModel(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)

	first, err := client.beginRequest(t.Context(), Request{Body: &core.ChatRequest{Model: "body-model"}}, false)
	require.NoError(t, err)
	require.Equal(t, "body-model", first.requestInfo.Model)
	require.NotSame(t, client.circuitBreaker, first.breaker)

	client.finishRequest(first, http.StatusOK, nil)

	// An explicit Request.Model wins over the body, and distinct models stay apart.
	labelled, err := client.beginRequest(t.Context(), Request{Model: "explicit", Body: &core.ChatRequest{Model: "body-model"}}, false)
	require.NoError(t, err)
	require.Equal(t, "explicit", labelled.requestInfo.Model)
	require.NotSame(t, first.breaker, labelled.breaker)

	client.finishRequest(labelled, http.StatusOK, nil)

	repeat, err := client.beginRequest(t.Context(), Request{Body: &core.ChatRequest{Model: "body-model"}}, false)
	require.NoError(t, err)
	require.Equal(t, first.breaker, repeat.breaker)

	client.finishRequest(repeat, http.StatusOK, nil)
	require.Len(t, client.modelBreakers, 2)
}

func TestProviderScopeKeepsASingleBreaker(t *testing.T) {
	cfg := DefaultConfig("test", "")
	client := New(cfg, nil)
	for _, model := range []string{"model1", "model2"} {
		got := client.breakerForModel(model)
		require.Equal(t, client.circuitBreaker, got, "%s must share the provider breaker under the default scope", model)

		// Releasing the provider breaker is a no-op, not a bad bookkeeping entry.
		client.releaseModelBreaker(model, client.circuitBreaker)
	}
	require.Empty(t, client.modelBreakers)
}
