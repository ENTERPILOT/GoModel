package providers

import (
	"log/slog"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// modelFilter is the resolved form of config.ModelFilter, evaluated against one
// provider's inventory after metadata enrichment.
type modelFilter struct {
	include         []string
	exclude         []string
	maxPricePerMtok *float64
}

// newModelFilter resolves cfg. The second return value is false when the filter
// would keep every model, so callers can skip storing and evaluating it.
func newModelFilter(cfg config.ModelFilter) (modelFilter, bool) {
	normalized := cfg.Normalize()
	if normalized.Empty() {
		return modelFilter{}, false
	}
	return modelFilter{
		include:         normalized.Include,
		exclude:         normalized.Exclude,
		maxPricePerMtok: normalized.MaxPricePerMtok,
	}, true
}

// keep reports whether model survives the filter.
func (f modelFilter) keep(model core.Model) bool {
	modelID := strings.TrimSpace(model.ID)
	if len(f.include) > 0 && !matchesAnyGlob(f.include, modelID) {
		return false
	}
	if matchesAnyGlob(f.exclude, modelID) {
		return false
	}
	if f.maxPricePerMtok == nil {
		return true
	}
	price, priced := modelMaxPricePerMtok(model)
	// An unpriced model is dropped rather than admitted: a cost cap that lets
	// models of unknown price through is not a cap.
	return priced && price <= *f.maxPricePerMtok
}

// modelMaxPricePerMtok returns a model's highest per-million-token rate — the
// larger of its input and output rate — and whether it is priced at all. Rates
// come from the model registry, from the provider's own listing, or from
// operator metadata overrides, whichever enrichment resolved last.
func modelMaxPricePerMtok(model core.Model) (float64, bool) {
	if model.Metadata == nil || model.Metadata.Pricing == nil {
		return 0, false
	}
	pricing := model.Metadata.Pricing
	price, priced := 0.0, false
	if pricing.InputPerMtok != nil {
		price, priced = *pricing.InputPerMtok, true
	}
	if pricing.OutputPerMtok != nil && (!priced || *pricing.OutputPerMtok > price) {
		price, priced = *pricing.OutputPerMtok, true
	}
	return price, priced
}

// filterProviderModelMaps drops the models each provider's filter excludes and
// returns how many were dropped.
func (r *ModelRegistry) filterProviderModelMaps(modelsByProvider map[string]map[string]*ModelInfo) int {
	return filterProviderModelMaps(r.snapshotProviderModelFilters(), modelsByProvider)
}

// filterProviderModelMaps applies filters to modelsByProvider. It runs after
// metadata enrichment so price rules see resolved pricing, and it never removes
// a provider's map: a filter that matches nothing leaves the provider present
// with an empty inventory rather than looking like a failed refresh.
func filterProviderModelMaps(filters map[string]modelFilter, modelsByProvider map[string]map[string]*ModelInfo) int {
	if len(filters) == 0 {
		return 0
	}

	dropped := 0
	for providerName, providerModels := range modelsByProvider {
		filter, ok := filters[providerName]
		if !ok || len(providerModels) == 0 {
			continue
		}
		before := len(providerModels)
		for modelID, info := range providerModels {
			if info == nil || !filter.keep(info.Model) {
				delete(providerModels, modelID)
			}
		}
		removed := before - len(providerModels)
		if removed == 0 {
			continue
		}
		dropped += removed
		if len(providerModels) == 0 {
			slog.Warn("model_filter removed every model of a provider",
				"provider", providerName,
				"dropped", removed,
			)
			continue
		}
		slog.Debug("model_filter applied",
			"provider", providerName,
			"kept", len(providerModels),
			"dropped", removed,
		)
	}
	return dropped
}

// matchesAnyGlob reports whether value matches at least one pattern.
func matchesAnyGlob(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matchesGlob(pattern, value) {
			return true
		}
	}
	return false
}

// matchesGlob reports whether value matches pattern, case-insensitively: `*`
// matches any run of characters and `?` exactly one. Unlike filepath.Match,
// `*` also matches `/`, because model IDs are paths ("deepseek/deepseek-r1")
// rather than file paths and `*:free` must match across the vendor segment.
func matchesGlob(pattern, value string) bool {
	p := []rune(strings.ToLower(pattern))
	v := []rune(strings.ToLower(value))

	// Standard backtracking glob match: on a mismatch, resume at the character
	// after the last `*` with one more character consumed by it.
	var pi, vi int
	star, starMatch := -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]):
			pi++
			vi++
		case pi < len(p) && p[pi] == '*':
			star, starMatch = pi, vi
			pi++
		case star >= 0:
			starMatch++
			pi, vi = star+1, starMatch
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
