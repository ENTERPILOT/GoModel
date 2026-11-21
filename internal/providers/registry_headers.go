package providers

import "github.com/enterpilot/gomodel/internal/core"

// SetProviderHeaders records the static headers a registered provider
// instance was configured with (providers.<name>.headers), for the calls that
// do not go through the provider's own HTTP client: realtime dials and
// signaling. An empty set clears them. Unregistering or replacing the
// instance drops them.
func (r *ModelRegistry) SetProviderHeaders(provider core.Provider, headers map[string]string) {
	if provider == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(headers) == 0 {
		delete(r.providerHeaders, provider)
		return
	}
	if r.providerHeaders == nil {
		r.providerHeaders = make(map[core.Provider]map[string]string)
	}
	r.providerHeaders[provider] = headers
}

// ProviderHeaders returns the static headers of a registered provider
// instance, nil when it has none. The map is shared: callers must not modify
// it, and must never log its values.
func (r *ModelRegistry) ProviderHeaders(provider core.Provider) map[string]string {
	if r == nil || provider == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerHeaders[provider]
}
