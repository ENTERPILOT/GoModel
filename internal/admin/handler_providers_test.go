package admin

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/providers"
)

// TestClassifyProviderStatus_HealthyForAllowlistInventory locks in the
// admin-endpoint behavior fixed alongside the registry change that makes
// allowlist mode set LastModelFetchSuccessAt. Before the fix, an allowlist
// provider serving real traffic appeared as status=degraded / label=Starting
// because the classifier treated LastModelFetchSuccessAt==nil as "still
// loading cached models". Now the classifier correctly reports healthy.
func TestClassifyProviderStatus_HealthyForAllowlistInventory(t *testing.T) {
	now := time.Now().UTC()
	cfg := providers.SanitizedProviderConfig{Name: "bedrock", Type: "bedrock"}
	runtime := providers.ProviderRuntimeSnapshot{
		Name:                    "bedrock",
		Type:                    "bedrock",
		Registered:              true,
		RegistryInitialized:     true,
		DiscoveredModelCount:    1,
		LastModelFetchAt:        &now,
		LastModelFetchSuccessAt: &now,
	}

	status, label, _, _ := classifyProviderStatus(cfg, runtime)
	if status != "healthy" {
		t.Fatalf("status = %q, want healthy", status)
	}
	if label != "Healthy" {
		t.Fatalf("label = %q, want Healthy", label)
	}
}

// A provider retired from load balancing by a failed availability probe has a
// clean model-fetch record but must not be reported healthy: the routing layer
// is actively skipping it.
func TestClassifyProviderStatus_StaleInventoryIsDegraded(t *testing.T) {
	now := time.Now().UTC()
	cfg := providers.SanitizedProviderConfig{Name: "openai", Type: "openai"}
	runtime := providers.ProviderRuntimeSnapshot{
		Name:                    "openai",
		Type:                    "openai",
		Registered:              true,
		RegistryInitialized:     true,
		DiscoveredModelCount:    3,
		LastModelFetchAt:        &now,
		LastModelFetchSuccessAt: &now,
		LastAvailabilityCheckAt: &now,
		LastAvailabilityError:   "connection refused",
		InventoryStale:          true,
	}

	status, label, reason, lastError := classifyProviderStatus(cfg, runtime)
	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
	}
	if label != "Degraded" {
		t.Fatalf("label = %q, want Degraded", label)
	}
	if reason == "" {
		t.Fatal("reason empty, want stale-inventory explanation")
	}
	if lastError != "connection refused" {
		t.Fatalf("lastError = %q, want availability error surfaced", lastError)
	}
}
