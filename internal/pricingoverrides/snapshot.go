package pricingoverrides

import (
	"fmt"
	"sort"

	"gomodel/internal/modelselectors"
)

type compiledOverride struct {
	override Override
}

type snapshot struct {
	order        []string
	bySelector   map[string]Override
	global       compiledOverride
	hasGlobal    bool
	modelWide    map[string]compiledOverride
	providerWide map[string]compiledOverride
	exact        map[string]compiledOverride
}

func emptySnapshot() snapshot {
	return snapshot{
		order:        []string{},
		bySelector:   map[string]Override{},
		modelWide:    map[string]compiledOverride{},
		providerWide: map[string]compiledOverride{},
		exact:        map[string]compiledOverride{},
	}
}

func (s *Service) buildSnapshot(overrides []Override) (snapshot, error) {
	next := emptySnapshot()
	next.order = make([]string, 0, len(overrides))
	next.bySelector = make(map[string]Override, len(overrides))

	for _, override := range overrides {
		normalized, err := normalizeStoredOverride(override)
		if err != nil {
			return snapshot{}, fmt.Errorf("load model pricing override %q: %w", override.Selector, err)
		}
		next.order = append(next.order, normalized.Selector)
		next.bySelector[normalized.Selector] = normalized

		compiled := compiledOverride{override: normalized}
		switch normalized.ScopeKind() {
		case modelselectors.ScopeGlobal:
			next.global = compiled
			next.hasGlobal = true
		case modelselectors.ScopeProviderModel:
			next.exact[modelselectors.ExactMatchKey(normalized.ProviderName, normalized.Model)] = compiled
		case modelselectors.ScopeProvider:
			next.providerWide[normalized.ProviderName] = compiled
		default:
			next.modelWide[normalized.Model] = compiled
		}
	}
	sort.Strings(next.order)
	return next, nil
}

func (snap snapshot) matchingOverride(providerName, model string) (compiledOverride, bool) {
	if key := modelselectors.ExactMatchKey(providerName, model); key != "" {
		if exact, ok := snap.exact[key]; ok {
			return exact, true
		}
	}
	if model != "" {
		if modelWide, ok := snap.modelWide[model]; ok {
			return modelWide, true
		}
	}
	if providerName != "" {
		if providerWide, ok := snap.providerWide[providerName]; ok {
			return providerWide, true
		}
	}
	if snap.hasGlobal {
		return snap.global, true
	}
	return compiledOverride{}, false
}

func snapshotOverrides(snap snapshot) []Override {
	result := make([]Override, 0, len(snap.order))
	for _, selector := range snap.order {
		result = append(result, overrideClone(snap.bySelector[selector]))
	}
	return result
}

func upsertOverride(overrides []Override, next Override) []Override {
	for i := range overrides {
		if overrides[i].Selector == next.Selector {
			overrides[i] = overrideClone(next)
			return overrides
		}
	}
	return append(overrides, overrideClone(next))
}

func deleteOverride(overrides []Override, selector string) []Override {
	result := make([]Override, 0, len(overrides))
	for _, override := range overrides {
		if override.Selector == selector {
			continue
		}
		result = append(result, overrideClone(override))
	}
	return result
}
