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
			if err := request("model1"); err == nil {
				t.Fatal("expected exhausted retries")
			}
			if len(calls) != 3 {
				t.Fatalf("calls = %v, want three model1 attempts", calls)
			}
			if err := request("model2"); err != nil {
				t.Fatal(err)
			}
			if err := request("model1"); err == nil {
				t.Fatal("expected open breaker")
			}
			if len(calls) != 4 || calls[3] != "/model2" {
				t.Fatalf("calls = %v", calls)
			}
			if states[0] != "open" || states[1] != "closed" || states[2] != "open" {
				t.Fatalf("states = %v", states)
			}
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
			if err := client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil); err == nil {
				t.Fatal("expected failure")
			}
			if calls != tc.wantCalls || client.circuitBreaker.State() != tc.wantState {
				t.Fatalf("calls=%d state=%s", calls, client.circuitBreaker.State())
			}
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
			if got := client.breakerForModel("model1"); got != want {
				t.Error("model breaker was replaced")
			}
		})
	}
	wg.Wait()
	if client.breakerForModel("") != client.circuitBreaker {
		t.Fatal("discovery should use provider breaker")
	}
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
		if err := client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil); err == nil {
			t.Fatal("expected timeout error")
		}
		if got := client.circuitBreaker.State(); got != wantState {
			t.Fatalf("breaker state=%s, want %s", got, wantState)
		}
	}
	if calls != 6 {
		t.Fatalf("calls=%d, want two sequences of three attempts", calls)
	}
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
	if err := client.Do(context.Background(), Request{Method: "GET", Endpoint: "/test"}, nil); err == nil {
		t.Fatal("expected rate limit response")
	}
	if got := client.circuitBreaker.State(); got != "closed" {
		t.Fatalf("state=%s, excluded status must not reopen breaker", got)
	}
}

func TestNilStatusPoliciesFallBackToDefaults(t *testing.T) {
	// A programmatic caller that never sets the lists must still get the
	// documented defaults rather than an empty, never-matching policy.
	client := New(Config{ProviderName: "test"}, nil)
	if client.configErr != nil {
		t.Fatal(client.configErr)
	}
	for _, status := range config.DefaultRetryConfig().RetryOnStatuses {
		code, err := strconv.Atoi(status)
		if err != nil {
			t.Fatal(err)
		}
		if !client.isRetryable(code) {
			t.Fatalf("%d must be retryable by default", code)
		}
	}
	for _, status := range []int{400, 404, 500} {
		if client.isRetryable(status) {
			t.Fatalf("%d is not a default retry trigger", status)
		}
	}
	for _, status := range []int{429, 500, 599} {
		if !client.shouldTripCircuitBreaker(status) {
			t.Fatalf("%d must trip the breaker by default", status)
		}
	}
	for _, status := range []int{200, 400, 404} {
		if client.shouldTripCircuitBreaker(status) {
			t.Fatalf("%d must not trip the breaker", status)
		}
	}
}

func TestEmptyStatusPoliciesDisableStatusTriggers(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.Retry.RetryOnStatuses = []string{}
	cfg.CircuitBreaker.FailureOnStatuses = []string{}
	client := New(cfg, nil)
	if client.configErr != nil {
		t.Fatal(client.configErr)
	}
	for _, status := range []int{429, 500, 503, 524} {
		if client.isRetryable(status) || client.shouldTripCircuitBreaker(status) {
			t.Fatalf("%d must not trigger anything once the lists are explicitly empty", status)
		}
	}
}
