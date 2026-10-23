package app

import (
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// jevProviderType is the provider type of Jev and Kev, which serve only
// decisions.
const jevProviderType = "jev"

// pluginDecider is what plugins ask decision models through: the internal
// Decisions executor answers, and the provider catalog and virtual models
// say which models answer decisions only.
type pluginDecider struct {
	*server.InternalDecisionsExecutor
	router *providers.Router
	// virtualModels may be nil.
	virtualModels *virtualmodels.Service
}

var _ plugins.Decider = pluginDecider{}

// DecisionModel reports whether model answers decisions only. A redirect is
// one when every target is; it is read from its definition rather than
// resolved, since resolving picks a target and advances load balancing.
func (d pluginDecider) DecisionModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || d.router == nil {
		return false
	}
	if d.virtualModels != nil {
		if vm, ok := d.virtualModels.Get(model); ok && vm.IsRedirect() {
			if !vm.Enabled {
				return false
			}
			for _, target := range vm.Targets {
				if !d.concreteDecisionModel(core.ModelSelector{Provider: target.Provider, Model: target.Model}.QualifiedModel()) {
					return false
				}
			}
			return true
		}
	}
	return d.concreteDecisionModel(model)
}

// concreteDecisionModel reports whether a provider model answers decisions
// only: any model of a jev provider (a pinned version the catalog does not
// list included), or one the catalog describes as a utility model with no
// generation mode, as OpenRouter's decision models are.
func (d pluginDecider) concreteDecisionModel(model string) bool {
	selector, _, err := d.router.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		if selector, err = core.ParseModelSelector(model, ""); err != nil {
			return false
		}
	}
	if provider := selector.Provider; provider != "" {
		// The segment names a provider instance, or else a provider type.
		providerType := d.router.GetProviderTypeForName(provider)
		if providerType == "" {
			providerType = provider
		}
		if providerType == jevProviderType {
			return true
		}
	}
	info, ok := d.router.LookupModel(selector.QualifiedModel())
	if !ok || info == nil || info.Metadata == nil || len(info.Metadata.Modes) > 0 {
		return false
	}
	return slices.Contains(info.Metadata.Categories, core.CategoryUtility)
}
