package providers

import (
	"maps"
	"reflect"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// metadataOverrideLayers holds the operator metadata overrides, both keyed by
// provider instance name -> raw model ID. Enrichment applies config first and
// dashboard second, so a dashboard-managed field wins over the same field in
// config.yaml while fields it leaves unset fall through.
type metadataOverrideLayers struct {
	config    map[string]map[string]*core.ModelMetadata
	dashboard map[string]map[string]*core.ModelMetadata
	// dashboardRevision is the registry's dashboardMetadataRevision when the
	// layers were snapshotted (see publishEnrichedInventoryLocked).
	dashboardRevision uint64
}

// apply layers both override sets onto enriched models using the
// applyConfigMetadataOverrides replacements protocol.
func (l metadataOverrideLayers) apply(
	modelsByProvider map[string]map[string]*ModelInfo,
	replacements map[*ModelInfo]*ModelInfo,
) int {
	applied := applyConfigMetadataOverrides(l.config, modelsByProvider, replacements)
	return applied + applyConfigMetadataOverrides(l.dashboard, modelsByProvider, replacements)
}

// snapshotMetadataOverrides copies both override layers' outer and inner maps
// for use outside the lock. The *core.ModelMetadata values are shared, which
// is safe because both setters deep-clone on insertion and the registry never
// hands those values back out.
func (r *ModelRegistry) snapshotMetadataOverrides() metadataOverrideLayers {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return metadataOverrideLayers{
		config:            cloneOverrideIndex(r.configMetadataOverrides),
		dashboard:         cloneOverrideIndex(r.dashboardMetadataOverrides),
		dashboardRevision: r.dashboardMetadataRevision,
	}
}

func cloneOverrideIndex(in map[string]map[string]*core.ModelMetadata) map[string]map[string]*core.ModelMetadata {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]*core.ModelMetadata, len(in))
	for provider, inner := range in {
		innerCopy := make(map[string]*core.ModelMetadata, len(inner))
		maps.Copy(innerCopy, inner)
		out[provider] = innerCopy
	}
	return out
}

// SetDashboardMetadataOverrides replaces the dashboard-managed metadata
// overrides (provider instance name -> raw model ID -> metadata) and
// re-enriches the catalog so the change is live immediately: model
// categories, capabilities and token limits read from the registry, the
// dashboard and /v1/models all see it. An unchanged set is a no-op, so a
// periodic storage refresh does not re-enrich every tick.
//
// Unlike config overrides these survive a provider being unregistered: they
// are persisted by selector and apply again when the provider returns.
func (r *ModelRegistry) SetDashboardMetadataOverrides(overrides map[string]map[string]*core.ModelMetadata) {
	next := make(map[string]map[string]*core.ModelMetadata, len(overrides))
	for providerName, models := range overrides {
		providerName = strings.TrimSpace(providerName)
		if providerName == "" {
			continue
		}
		for modelID, meta := range models {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" || metadataOverrideEmpty(meta) {
				continue
			}
			if next[providerName] == nil {
				next[providerName] = make(map[string]*core.ModelMetadata)
			}
			next[providerName][modelID] = meta.Clone()
		}
	}
	if len(next) == 0 {
		next = nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if reflect.DeepEqual(r.dashboardMetadataOverrides, next) {
		return
	}
	r.dashboardMetadataOverrides = next
	r.dashboardMetadataRevision++
	_ = r.enrichModelsLocked()
}

// publishEnrichedInventoryLocked publishes r.discoveredByProvider after a
// caller replaced it with an inventory enriched outside the lock against the
// dashboard overrides at enrichedAt. A save that landed since then would be
// published over with the older values and stay lost, because an unchanged
// set is a no-op in SetDashboardMetadataOverrides, so enrichment runs again
// here instead. Caller must hold r.mu for writing.
func (r *ModelRegistry) publishEnrichedInventoryLocked(enrichedAt uint64) {
	if enrichedAt != r.dashboardMetadataRevision && len(r.discoveredByProvider) > 0 {
		_ = r.enrichModelsLocked()
		return
	}
	r.publishFilteredInventoryLocked()
}

// resetToDiscoveredMetadata restores every model's metadata to what its
// provider reported. enrichModelsLocked runs it when no model list is loaded
// (catalog off or not yet fetched), where catalog enrichment would otherwise
// be the step that rebuilds metadata from pristine inputs: without it a
// removed override would stay merged into the published metadata.
func resetToDiscoveredMetadata(
	modelsByProvider map[string]map[string]*ModelInfo,
	replacements map[*ModelInfo]*ModelInfo,
) {
	for _, providerModels := range modelsByProvider {
		accessor := &registryAccessor{models: providerModels, replacements: replacements}
		for modelID, info := range providerModels {
			if reflect.DeepEqual(info.Model.Metadata, info.Discovered) {
				continue
			}
			accessor.SetMetadata(modelID, info.Discovered.Clone())
		}
	}
}
