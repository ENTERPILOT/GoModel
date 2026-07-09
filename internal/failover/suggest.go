package failover

import (
	"slices"
	"strings"

	"gomodel/config"
	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// GenerateSuggestions builds dashboard failover-rule suggestions for every
// text-generation-capable model in the registry, optionally filtered to one
// source model (matched by selector, bare model ID, or provider-qualified ID).
// It returns an empty slice when the registry is nil or nothing qualifies.
func GenerateSuggestions(registry Registry, rules RuleProvider, primaryModel string) []View {
	resolver := NewResolverWithRuleProvider(config.FailoverConfig{Enabled: true}, registry, rules)
	if resolver == nil {
		return []View{}
	}
	suggestions := make([]View, 0)
	for _, model := range registry.ListModelsWithProvider() {
		if !modelSupportsCategory(model.Model.Metadata, core.CategoryTextGeneration) {
			continue
		}
		source := strings.TrimSpace(model.Selector)
		if source == "" {
			continue
		}
		if primaryModel != "" && !sourceMatchesModel(primaryModel, source, model) {
			continue
		}
		resolution := &core.RequestModelResolution{
			Requested: core.NewRequestedModelSelector(model.Model.ID, model.ProviderName),
			ResolvedSelector: core.ModelSelector{
				Provider: model.ProviderName,
				Model:    model.Model.ID,
			},
			ProviderName: model.ProviderName,
			ProviderType: model.ProviderType,
		}
		candidates := resolver.SuggestFailovers(resolution, core.OperationChatCompletions)
		if len(candidates) == 0 {
			continue
		}
		targets := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			targets = append(targets, candidate.QualifiedModel())
		}
		suggestions = append(suggestions, View{
			Source:        source,
			Targets:       targets,
			Enabled:       true,
			ManagedSource: ManagedSourceDashboard,
		})
	}
	return suggestions
}

func sourceMatchesModel(filter string, source string, model providers.ModelWithProvider) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	if filter == source || filter == strings.TrimSpace(model.Model.ID) {
		return true
	}
	if model.ProviderName != "" && filter == model.ProviderName+"/"+model.Model.ID {
		return true
	}
	return false
}

func modelSupportsCategory(meta *core.ModelMetadata, category core.ModelCategory) bool {
	if meta == nil || len(meta.Categories) == 0 {
		return true
	}
	return slices.Contains(meta.Categories, category)
}
