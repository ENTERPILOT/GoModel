package providers

import (
	"context"
	"log/slog"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// allowlistMetadataTimeout bounds the /models call an allowlist makes for
// metadata, so a slow upstream cannot hold up the provider's refresh: the
// configured list is served either way.
const allowlistMetadataTimeout = 10 * time.Second

// fetchAllowlistMetadata lists the provider's models to enrich an allowlist
// with what the provider reports (context length, pricing, model type). It
// never adds models: the inventory is the configured list, narrowed only by a
// model filter (whose price cap can read these prices). A failure is not a
// provider failure: health keeps following the availability check, and the
// caller falls back to the last listing. It returns nil when the listing
// fails or the provider has no /models endpoint.
func fetchAllowlistMetadata(ctx context.Context, provider core.Provider, providerName string) *core.ModelsResponse {
	ctx, cancel := context.WithTimeout(llmclient.WithoutCircuitBreaker(ctx), allowlistMetadataTimeout)
	defer cancel()
	resp, err := provider.ListModels(ctx)
	if err == nil {
		return resp
	}
	if !modelListingUnsupported(err) {
		slog.Warn("upstream ListModels failed; serving the configured allowlist without provider metadata",
			"provider", providerName,
			"error", err,
		)
	}
	return nil
}

// SetAllowlistUpstreamMetadata controls whether providers in allowlist mode
// call /models to enrich their configured models. It follows
// models.allowlist_upstream_metadata, which offline mode forces off.
func (r *ModelRegistry) SetAllowlistUpstreamMetadata(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowlistMetadata = enabled
}

func (r *ModelRegistry) allowlistMetadataEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allowlistMetadata
}

// lastProviderListings rebuilds, for each named provider with a configured
// list, what it reported in its current inventory, so an allowlist whose
// metadata call fails keeps the metadata of the last successful one.
func (r *ModelRegistry) lastProviderListings(names []string, configuredModels map[string][]string) map[string]*core.ModelsResponse {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*core.ModelsResponse)
	for _, name := range names {
		if len(configuredModels[name]) == 0 || len(r.discoveredByProvider[name]) == 0 {
			continue
		}
		out[name] = listingFromInventory(r.discoveredByProvider[name])
	}
	return out
}

// listingFromInventory turns an inventory back into the listing the provider
// reported: each model with the metadata the provider itself supplied, not
// what enrichment added on top.
func listingFromInventory(inventory map[string]*ModelInfo) *core.ModelsResponse {
	listed := &core.ModelsResponse{Object: "list", Data: make([]core.Model, 0, len(inventory))}
	for _, info := range inventory {
		if info == nil {
			continue
		}
		model := info.Model
		model.Metadata = info.Discovered.Clone()
		listed.Data = append(listed.Data, model)
	}
	return listed
}
