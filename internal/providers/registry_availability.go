package providers

import (
	"strings"
	"time"
)

// failureMessage returns the trimmed text of a failure for runtime state. An
// empty stored message means "no failure", so a message that is empty or only
// whitespace becomes a fixed marker instead of reading as success.
func failureMessage(message string) string {
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		return trimmed
	}
	return "unknown error"
}

// optionalFailureMessage is failureMessage for fields where "" means no
// failure occurred.
func optionalFailureMessage(message string) string {
	if message == "" {
		return ""
	}
	return failureMessage(message)
}

// RecordAvailabilityCheck stores the latest startup or explicit availability
// probe result for a configured provider name.
func (r *ModelRegistry) RecordAvailabilityCheck(providerName string, err error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.providerRuntime[providerName]
	state.registered = true
	state.lastAvailabilityCheckAt = time.Now().UTC()
	if err != nil {
		state.lastAvailabilityError = failureMessage(err.Error())
	} else {
		state.lastAvailabilityOKAt = state.lastAvailabilityCheckAt
		state.lastAvailabilityError = ""
	}
	r.providerRuntime[providerName] = state
}

// markProviderInventoryStale flags providerName's carried inventory as stale
// after a failed live probe, but only while at least one other registered
// provider is healthy. Without a healthy alternative, skipping the provider
// would leave virtual-model resolution with no target at all (a 404 for the
// alias) — routing to it and failing with the provider's 502/503 is the more
// honest degradation, and it keeps single-provider deployments and
// control-plane-only outages routable.
func (r *ModelRegistry) markProviderInventoryStale(providerName string) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.modelsByProvider[providerName]) == 0 || r.providerRuntime[providerName].inventoryStale {
		return
	}

	healthyAlternative := false
	for _, provider := range r.providers {
		name := r.providerNames[provider]
		if name == "" || name == providerName {
			continue
		}
		state := r.providerRuntime[name]
		if state.registered && !state.inventoryStale &&
			state.lastModelFetchError == "" &&
			state.lastAvailabilityError == "" &&
			len(r.modelsByProvider[name]) > 0 {
			healthyAlternative = true
			break
		}
	}
	if !healthyAlternative {
		return
	}

	state := r.providerRuntime[providerName]
	state.inventoryStale = true
	r.providerRuntime[providerName] = state
	r.models = rebuildGlobalModelMap(r.modelsByProvider, r.freshFirstProviderOrderLocked())
	r.invalidateSortedCaches()
}

// FailedProviderNames returns configured provider names whose latest model
// refresh attempt or availability probe failed. The background recheck loop
// uses this to re-probe only the providers that are currently down. The
// availability side matters: a request-time refresh can fail the availability
// gate (marking the inventory stale) without ever attempting a model fetch,
// and such a provider must still be re-probed to detect its recovery.
func (r *ModelRegistry) FailedProviderNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0)
	for _, provider := range r.providers {
		providerName := r.providerNames[provider]
		if providerName == "" {
			continue
		}
		state := r.providerRuntime[providerName]
		if state.lastModelFetchError == "" && state.lastAvailabilityError == "" {
			continue
		}
		names = append(names, providerName)
	}
	return names
}

// ProviderRuntimeSnapshots returns runtime diagnostics for configured providers
// keyed by configured provider name.
func (r *ModelRegistry) ProviderRuntimeSnapshots() []ProviderRuntimeSnapshot {
	r.mu.RLock()
	result := make([]ProviderRuntimeSnapshot, 0, len(r.providers))
	for _, provider := range r.providers {
		providerName := r.providerNames[provider]
		if providerName == "" {
			continue
		}
		state := r.providerRuntime[providerName]
		result = append(result, ProviderRuntimeSnapshot{
			Name:                    providerName,
			Type:                    r.providerTypes[provider],
			Registered:              state.registered,
			DiscoveredModelCount:    len(r.modelsByProvider[providerName]),
			LastModelFetchAt:        timePtrUTC(state.lastModelFetchAt),
			LastModelFetchSuccessAt: timePtrUTC(state.lastModelFetchSuccessAt),
			LastModelFetchError:     state.lastModelFetchError,
			LastAvailabilityCheckAt: timePtrUTC(state.lastAvailabilityCheckAt),
			LastAvailabilityOKAt:    timePtrUTC(state.lastAvailabilityOKAt),
			LastAvailabilityError:   state.lastAvailabilityError,
			InventoryStale:          state.inventoryStale,
		})
	}
	r.mu.RUnlock()

	initialized := r.IsInitialized()
	for i := range result {
		result[i].RegistryInitialized = initialized
		result[i].UsingCachedModels = result[i].DiscoveredModelCount > 0 &&
			!initialized &&
			result[i].LastModelFetchSuccessAt == nil
	}

	return result
}
