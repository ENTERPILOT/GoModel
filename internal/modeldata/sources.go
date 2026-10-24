package modeldata

import "github.com/enterpilot/gomodel/internal/core"

// Layer names reported by MetadataSources.
const (
	// MetadataSourceConfig is a config.yaml metadata override.
	MetadataSourceConfig = "config"
	// MetadataSourceProvider is what the provider reported in its own listing.
	MetadataSourceProvider = "provider"
	// MetadataSourceCatalog is the model list (ai-model-list) entry.
	MetadataSourceCatalog = "catalog"
	// MetadataSourceInferred is a mode or category guessed from the model ID
	// because no layer knew the model.
	MetadataSourceInferred = "inferred"
)

// MetadataSources names, for every field set on effective, the layer that
// supplied it. Precedence mirrors enrichment: config over provider over
// catalog, with modes and categories inferred from the ID when no layer set
// them. Keys are the JSON field names; capabilities and rankings are reported
// per key ("capabilities.vision"). Pricing is left out: PricingSources
// already records it per price field.
func MetadataSources(effective, provider, catalog, config *core.ModelMetadata) map[string]string {
	if effective == nil {
		return nil
	}
	layers := []struct {
		name string
		meta *core.ModelMetadata
	}{
		{MetadataSourceConfig, config},
		{MetadataSourceProvider, provider},
		{MetadataSourceCatalog, catalog},
	}
	sources := make(map[string]string)
	pick := func(field string, set bool, has func(*core.ModelMetadata) bool) {
		if !set {
			return
		}
		for _, layer := range layers {
			if layer.meta != nil && has(layer.meta) {
				sources[field] = layer.name
				return
			}
		}
	}

	pick("display_name", effective.DisplayName != "", func(m *core.ModelMetadata) bool { return m.DisplayName != "" })
	pick("description", effective.Description != "", func(m *core.ModelMetadata) bool { return m.Description != "" })
	pick("family", effective.Family != "", func(m *core.ModelMetadata) bool { return m.Family != "" })
	pick("tags", len(effective.Tags) > 0, func(m *core.ModelMetadata) bool { return len(m.Tags) > 0 })
	pick("context_window", effective.ContextWindow != nil, func(m *core.ModelMetadata) bool { return m.ContextWindow != nil })
	pick("max_output_tokens", effective.MaxOutputTokens != nil, func(m *core.ModelMetadata) bool { return m.MaxOutputTokens != nil })

	pick("modes", len(effective.Modes) > 0, func(m *core.ModelMetadata) bool { return len(m.Modes) > 0 })
	if len(effective.Modes) > 0 && sources["modes"] == "" {
		sources["modes"] = MetadataSourceInferred
	}
	// A layer that declares modes without categories derives them, so the
	// categories come from the same layer as its modes.
	pick("categories", len(effective.Categories) > 0, func(m *core.ModelMetadata) bool {
		return len(m.Categories) > 0 || len(m.Modes) > 0
	})
	if len(effective.Categories) > 0 && sources["categories"] == "" {
		sources["categories"] = MetadataSourceInferred
	}

	for name := range effective.Capabilities {
		pick("capabilities."+name, true, func(m *core.ModelMetadata) bool {
			_, ok := m.Capabilities[name]
			return ok
		})
	}
	for name := range effective.Rankings {
		pick("rankings."+name, true, func(m *core.ModelMetadata) bool {
			_, ok := m.Rankings[name]
			return ok
		})
	}
	return sources
}
