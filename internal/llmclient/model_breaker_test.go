package llmclient

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
