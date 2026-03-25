package modeldata

import (
	"gomodel/internal/core"
)

// Resolve performs the three-layer merge to produce ModelMetadata for a given
// provider type and model ID. It looks up provider_models[providerType/modelID]
// first, then falls back to models[modelID]. Provider-model fields override
// base model fields where set.
// Returns nil if no match is found in the registry.
func Resolve(list *ModelList, providerType string, modelID string) *core.ModelMetadata {
	if list == nil {
		return nil
	}

	model, pm := resolveEntries(list, providerType, modelID)
	if model == nil && pm == nil {
		return nil
	}

	return buildMetadata(model, pm)
}

func resolveEntries(list *ModelList, providerType string, modelID string) (*ModelEntry, *ProviderModelEntry) {
	if model, pm := resolveDirect(list, providerType, modelID); model != nil || pm != nil {
		return model, pm
	}
	if model, pm := resolveReverseProviderModelID(list, providerType, modelID); model != nil || pm != nil {
		return model, pm
	}
	return resolveAlias(list, providerType, modelID)
}

func resolveDirect(list *ModelList, providerType string, modelID string) (*ModelEntry, *ProviderModelEntry) {
	if providerType != "" {
		if entry, ok := list.ProviderModels[providerType+"/"+modelID]; ok {
			pm := entry
			return resolveModelRef(list, providerType, pm.ModelRef, &pm)
		}
	}
	if entry, ok := list.Models[modelID]; ok {
		model := entry
		return &model, nil
	}
	return nil, nil
}

func resolveReverseProviderModelID(list *ModelList, providerType string, modelID string) (*ModelEntry, *ProviderModelEntry) {
	if providerType == "" || list.providerModelByActualID == nil {
		return nil, nil
	}
	compositeKey, ok := list.providerModelByActualID[providerType+"/"+modelID]
	if !ok {
		return nil, nil
	}
	pmEntry, ok := list.ProviderModels[compositeKey]
	if !ok {
		return nil, nil
	}
	pm := pmEntry
	return resolveModelRef(list, providerType, pm.ModelRef, &pm)
}

func resolveAlias(list *ModelList, providerType string, modelID string) (*ModelEntry, *ProviderModelEntry) {
	if list.aliasTargetsByID == nil {
		return nil, nil
	}
	targets := list.aliasTargetsByID[modelID]
	if len(targets) == 0 {
		return nil, nil
	}
	modelRef, ok := selectAliasModelRef(list, providerType, targets)
	if !ok {
		return nil, nil
	}
	return resolveModelRef(list, providerType, modelRef, nil)
}

func selectAliasModelRef(list *ModelList, providerType string, targets []aliasTarget) (string, bool) {
	if len(targets) == 0 {
		return "", false
	}

	bestScoreByModelRef := make(map[string]int, len(targets))
	for _, target := range targets {
		score := aliasTargetScore(providerType, target)
		if score == 0 {
			continue
		}
		if score > bestScoreByModelRef[target.ModelRef] {
			bestScoreByModelRef[target.ModelRef] = score
		}
	}
	if len(bestScoreByModelRef) == 0 {
		return "", false
	}

	bestScore := 0
	bestRefs := make([]string, 0, len(bestScoreByModelRef))
	for modelRef, score := range bestScoreByModelRef {
		switch {
		case score > bestScore:
			bestScore = score
			bestRefs = []string{modelRef}
		case score == bestScore:
			bestRefs = append(bestRefs, modelRef)
		}
	}
	if len(bestRefs) == 1 {
		return bestRefs[0], true
	}

	if providerType != "" {
		withProviderOverride := make([]string, 0, len(bestRefs))
		for _, modelRef := range bestRefs {
			if _, ok := list.ProviderModels[providerType+"/"+modelRef]; ok {
				withProviderOverride = append(withProviderOverride, modelRef)
			}
		}
		if len(withProviderOverride) == 1 {
			return withProviderOverride[0], true
		}
	}

	return "", false
}

func aliasTargetScore(providerType string, target aliasTarget) int {
	switch {
	case target.ProviderType == "":
		return 1
	case providerType != "" && target.ProviderType == providerType:
		return 2
	default:
		return 0
	}
}

func resolveModelRef(list *ModelList, providerType, modelRef string, pm *ProviderModelEntry) (*ModelEntry, *ProviderModelEntry) {
	if pm == nil && providerType != "" {
		if entry, ok := list.ProviderModels[providerType+"/"+modelRef]; ok {
			providerModel := entry
			pm = &providerModel
		}
	}
	if entry, ok := list.Models[modelRef]; ok {
		model := entry
		return &model, pm
	}
	return nil, pm
}

// buildMetadata merges base model fields with provider-model overrides into ModelMetadata.
func buildMetadata(model *ModelEntry, pm *ProviderModelEntry) *core.ModelMetadata {
	meta := &core.ModelMetadata{}

	// Apply base model fields
	if model != nil {
		meta.DisplayName = model.DisplayName
		if model.Description != nil {
			meta.Description = *model.Description
		}
		if model.Family != nil {
			meta.Family = *model.Family
		}
		meta.Modes = model.Modes
		meta.Categories = core.CategoriesForModes(model.Modes)
		meta.Tags = model.Tags
		meta.ContextWindow = model.ContextWindow
		meta.MaxOutputTokens = model.MaxOutputTokens
		meta.Capabilities = model.Capabilities
		meta.Pricing = model.Pricing
	}

	// Apply provider_model overrides (non-nil fields win)
	if pm != nil {
		if pm.ContextWindow != nil {
			meta.ContextWindow = pm.ContextWindow
		}
		if pm.MaxOutputTokens != nil {
			meta.MaxOutputTokens = pm.MaxOutputTokens
		}
		if pm.Pricing != nil {
			meta.Pricing = pm.Pricing
		}
		if pm.Capabilities != nil {
			meta.Capabilities = pm.Capabilities
		}
	}

	return meta
}
