package llmclient

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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
		if breaker == nil {
			t.Fatal("idle entries should be evicted")
		}
		client.releaseModelBreaker(model, breaker)
	}
	if len(client.modelBreakers) > maxModelBreakers {
		t.Fatal("unbounded storage")
	}
	if client.breakerForModel("busy") != busy || client.breakerForModel("open") != open {
		t.Fatal("evicted protected state")
	}
	// Expired idle state is removed on the next new model lookup.
	key := sha256.Sum256([]byte(fmt.Sprintf("caller-model-%d", maxModelBreakers*2-1)))
	client.modelBreakers[key].lastUsed = time.Now().Add(-modelBreakerIdleTTL - time.Second)
	client.breakerForModel("new")
	if client.modelBreakers[key] != nil {
		t.Fatal("expired entry retained")
	}
}

func TestModelBreakerCapacityRejectsWithoutBypassingProtection(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	for i := range maxModelBreakers {
		client.breakerForModel(fmt.Sprint(i))
	}
	_, err := client.DoRaw(t.Context(), Request{Model: "overflow", Method: "GET", Endpoint: "/test"})
	if err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatalf("error=%v", err)
	}
	if len(client.modelBreakers) != maxModelBreakers {
		t.Fatal("capacity exceeded")
	}
}

func TestUnknownModelUsesProviderBreaker(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)
	scope, err := client.beginRequest(t.Context(), Request{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if scope.breaker != client.circuitBreaker || len(client.modelBreakers) != 0 {
		t.Fatal("unknown model allocated a model breaker")
	}
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
			if err == nil || !strings.Contains(err.Error(), "invalid resilience configuration") {
				t.Fatalf("error=%v", err)
			}
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

	if err := request("model1"); err == nil {
		t.Fatal("expected the upstream failure")
	}
	if err := request("model1"); err == nil || !strings.Contains(err.Error(), "circuit breaker is open") {
		t.Fatalf("error=%v, want the open model1 breaker to short-circuit", err)
	}
	// A healthy sibling model keeps serving traffic on its own breaker.
	if err := request("model2"); err != nil {
		t.Fatal(err)
	}

	failing = false
	breaker := client.breakerForModel("model1")
	client.releaseModelBreaker("model1", breaker)
	breaker.mu.Lock()
	breaker.lastFailure = time.Now().Add(-cfg.CircuitBreaker.Timeout - time.Second)
	breaker.mu.Unlock()

	if err := request("model1"); err != nil {
		t.Fatalf("half-open probe should have been admitted: %v", err)
	}
	if got := breaker.State(); got != "closed" {
		t.Fatalf("model1 breaker state=%s, want closed after a successful probe", got)
	}
	if model2 := client.breakerForModel("model2"); model2 == breaker || model2.State() != "closed" {
		t.Fatal("model2 must keep an independent, closed breaker")
	}
}

func TestModelBreakerKeyedByRequestBodyModel(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Scope = "model"
	client := New(cfg, nil)

	first, err := client.beginRequest(t.Context(), Request{Body: &core.ChatRequest{Model: "body-model"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.requestInfo.Model != "body-model" {
		t.Fatalf("model=%q, want it recovered from the request body", first.requestInfo.Model)
	}
	if first.breaker == client.circuitBreaker {
		t.Fatal("a body-derived model must get its own breaker")
	}
	client.finishRequest(first, http.StatusOK, nil)

	// An explicit Request.Model wins over the body, and distinct models stay apart.
	labelled, err := client.beginRequest(t.Context(), Request{Model: "explicit", Body: &core.ChatRequest{Model: "body-model"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if labelled.requestInfo.Model != "explicit" || labelled.breaker == first.breaker {
		t.Fatalf("model=%q shared breaker=%v", labelled.requestInfo.Model, labelled.breaker == first.breaker)
	}
	client.finishRequest(labelled, http.StatusOK, nil)

	repeat, err := client.beginRequest(t.Context(), Request{Body: &core.ChatRequest{Model: "body-model"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.breaker != first.breaker {
		t.Fatal("the same model must reuse its breaker")
	}
	client.finishRequest(repeat, http.StatusOK, nil)
	if len(client.modelBreakers) != 2 {
		t.Fatalf("breakers=%d, want one per distinct model", len(client.modelBreakers))
	}
}

func TestProviderScopeKeepsASingleBreaker(t *testing.T) {
	cfg := DefaultConfig("test", "")
	client := New(cfg, nil)
	for _, model := range []string{"model1", "model2"} {
		if got := client.breakerForModel(model); got != client.circuitBreaker {
			t.Fatalf("%s must share the provider breaker under the default scope", model)
		}
		// Releasing the provider breaker is a no-op, not a bad bookkeeping entry.
		client.releaseModelBreaker(model, client.circuitBreaker)
	}
	if len(client.modelBreakers) != 0 {
		t.Fatalf("breakers=%d, want none outside model scope", len(client.modelBreakers))
	}
}
