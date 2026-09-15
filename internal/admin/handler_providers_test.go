package admin

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/stretchr/testify/require"
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
	require.Equal(t, "healthy", status)
	require.Equal(t, "Healthy", label)
}

// A provider retired from load balancing by a failed availability probe has a
// clean model-fetch record but must not be reported healthy: the routing layer
// is actively skipping it and its models are hidden from the model list.
func TestClassifyProviderStatus_StaleInventoryIsUnhealthy(t *testing.T) {
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
	require.Equal(t, "unhealthy", status)
	require.Equal(t, "Offline", label)
	require.NotEmpty(t, reason)
	require.Equal(t, "connection refused", lastError)
}
