package llmclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusPoliciesAndModelBreakers(t *testing.T) {
	for _, status := range []int{429, 522, 524} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.URL.Path == "/model1" {
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			cfg := DefaultConfig("cloudflare", server.URL)
			cfg.Retry.MaxRetries = 2
			cfg.Retry.InitialBackoff = time.Nanosecond
			cfg.CircuitBreaker.Scope = "model"
			cfg.CircuitBreaker.FailureThreshold = 1
			client := New(cfg, nil)
			var states []string
			client.config.Hooks.OnRequestEnd = func(_ context.Context, info ResponseInfo) { states = append(states, info.CircuitState) }
			request := func(model string) error {
				return client.Do(context.Background(), Request{Method: "GET", Endpoint: "/" + model, Model: model}, nil)
			}
			require.Error(t, request("model1"))
			require.Len(t, calls, 3)
			err := request("model2")
			require.NoError(t, err)
			require.Error(t, request("model1"))
			require.Len(t, calls, 4)
			require.Equal(t, "/model2", calls[3])
			require.Equal(t, "open", states[0])
			require.Equal(t, "closed", states[1])
			require.Equal(t, "open", states[2], "states = %v", states)
		})
	}
}

func TestCustomAndEmptyStatusPolicies(t *testing.T) {
	for _, tc := range []struct {
		name              string
		retry, failure    []string
		status, wantCalls int
		wantState         string
	}{
		{"custom retry", []string{"5xx"}, []string{}, 500, 3, "closed"},
		{"empty retry", []string{}, []string{"524"}, 524, 1, "open"},
		{"excluded failure", nil, []string{"500"}, 429, 3, "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(tc.status) }))
			defer server.Close()
			cfg := DefaultConfig("test", server.URL)
			cfg.Retry.MaxRetries = 2
			cfg.Retry.InitialBackoff = time.Nanosecond
			cfg.Retry.RetryOnStatuses = tc.retry
			cfg.CircuitBreaker.FailureOnStatuses = tc.failure
			cfg.CircuitBreaker.FailureThreshold = 1
			client := New(cfg, nil)
			require.Error(t, client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil))
			require.Equal(t, tc.wantCalls, calls)
			require.Equal(t, tc.wantState, client.circuitBreaker.State())
		})
	}
}

func TestModelBreakerConcurrentLookup(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	want := client.breakerForModel("model1")
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			got := client.breakerForModel("model1")
			assert.Same(t, want, got)
		})
	}
	wg.Wait()
	require.Same(t, client.circuitBreaker, client.breakerForModel(""))
}

func TestBreakerCountsRetrySequenceOnce(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(524)
	}))
	defer server.Close()
	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 2
	cfg.Retry.InitialBackoff = time.Nanosecond
	cfg.CircuitBreaker.FailureThreshold = 2
	client := New(cfg, nil)
	for _, wantState := range []string{"closed", "open"} {
		require.Error(t, client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil))
		got := client.circuitBreaker.State()
		require.Equal(t, wantState, got)
	}
	require.Equal(t, 6, calls)
}

func TestExcludedStatusAllowsHalfOpenRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	defer server.Close()
	cfg := DefaultConfig("test", server.URL)
	cfg.CircuitBreaker.FailureOnStatuses = []string{"5xx"}
	cfg.CircuitBreaker.SuccessThreshold = 1
	client := New(cfg, nil)
	client.circuitBreaker.state = circuitOpen
	client.circuitBreaker.lastFailure = time.Now().Add(-cfg.CircuitBreaker.Timeout - time.Second)
	require.Error(t, client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil))
	got := client.circuitBreaker.State()
	require.Equal(t, "closed", got)
}

func TestNilStatusPoliciesFallBackToDefaults(t *testing.T) {
	// A programmatic caller that never sets the lists must still get the
	// documented defaults rather than an empty, never-matching policy.
	client := New(Config{ProviderName: "test"}, nil)
	require.NoError(t, client.configErr)

	for _, status := range config.DefaultRetryConfig().RetryOnStatuses {
		code, err := strconv.Atoi(status)
		require.NoError(t, err)
		require.True(t, client.isRetryable(code), "%d must be retryable by default", code)
	}
	for _, status := range []int{400, 404, 500} {
		require.False(t, client.isRetryable(status), "%d is not a default retry trigger", status)
	}
	for _, status := range []int{429, 500, 599} {
		require.True(t, client.shouldTripCircuitBreaker(status), "%d must trip the breaker by default", status)
	}
	for _, status := range []int{200, 400, 404} {
		require.False(t, client.shouldTripCircuitBreaker(status), "%d must not trip the breaker", status)
	}
}

func TestEmptyStatusPoliciesDisableStatusTriggers(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.Retry.RetryOnStatuses = []string{}
	cfg.CircuitBreaker.FailureOnStatuses = []string{}
	client := New(cfg, nil)
	require.NoError(t, client.configErr)

	for _, status := range []int{429, 500, 503, 524} {
		require.False(t, client.isRetryable(status))
		require.False(t, client.shouldTripCircuitBreaker(status), "%d must not trigger anything once the lists are explicitly empty", status)
	}
}
