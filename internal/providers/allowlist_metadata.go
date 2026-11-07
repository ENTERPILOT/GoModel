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
// never decides the inventory, which stays exactly the configured list, and a
// failure is not a provider failure: the configured models are served as they
// are and health keeps following the availability check. It returns nil when
// the listing fails or the provider has no /models endpoint.
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
	r.allowlistMetadataDisabled = !enabled
}

func (r *ModelRegistry) allowlistMetadataEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.allowlistMetadataDisabled
}
