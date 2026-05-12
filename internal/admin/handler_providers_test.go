package admin

import (
	"testing"
	"time"

	"gomodel/internal/providers"
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
