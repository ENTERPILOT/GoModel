package server

import (
	"strings"

	"gomodel/internal/core"
)

type passthroughProviderResolution struct {
	RouteProvider string
	ProviderType  string
	ProviderName  string
}

func resolvePassthroughProvider(provider core.RoutableProvider, routeProvider string) passthroughProviderResolution {
	routeProvider = strings.TrimSpace(routeProvider)
	if routeProvider == "" {
		return passthroughProviderResolution{}
	}

	if provider != nil {
		if named, ok := provider.(core.ProviderNameTypeResolver); ok {
			if providerType := strings.TrimSpace(named.GetProviderTypeForName(routeProvider)); providerType != "" {
				return passthroughProviderResolution{
					RouteProvider: routeProvider,
					ProviderType:  providerType,
					ProviderName:  routeProvider,
				}
			}
		}
	}

	return passthroughProviderResolution{
		RouteProvider: routeProvider,
		ProviderType:  routeProvider,
		ProviderName:  workflowProviderNameForType(provider, routeProvider),
	}
}

// passthroughAccessSelector derives an authorization selector from provider,
// which supplies provider name/type canonicalization, and info, which carries
// the passthrough route provider/model; it returns the selector and whether one
// could be built. It may intentionally return a core.ModelSelector with an
// empty Provider when resolvePassthroughProvider leaves ProviderName empty and
// none of the ProviderNameResolver.GetProviderName candidates resolve to a
// non-empty canonical name; downstream authorization/validation is expected to
// handle empty Provider values.
func passthroughAccessSelector(provider core.RoutableProvider, info *core.PassthroughRouteInfo) (core.ModelSelector, bool) {
	if info == nil {
		return core.ModelSelector{}, false
	}

	model := strings.TrimSpace(info.Model)
	if model == "" {
		return core.ModelSelector{}, false
	}

	routeProvider := strings.TrimSpace(info.Provider)
	resolvedProvider := resolvePassthroughProvider(provider, routeProvider)
	providerName := strings.TrimSpace(resolvedProvider.ProviderName)

	if named, ok := provider.(core.ProviderNameResolver); ok {
		candidates := make([]string, 0, 3)
		if routeProvider != "" {
			candidates = append(candidates, routeProvider+"/"+model)
		}
		if resolvedProvider.ProviderType != "" && resolvedProvider.ProviderType != routeProvider {
			candidates = append(candidates, resolvedProvider.ProviderType+"/"+model)
		}
		candidates = append(candidates, model)

		for _, candidate := range candidates {
			if canonical := strings.TrimSpace(named.GetProviderName(candidate)); canonical != "" {
				providerName = canonical
				break
			}
		}
	}

	return core.ModelSelector{
		Provider: providerName,
		Model:    model,
	}, true
}
