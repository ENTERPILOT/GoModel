package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// Decisions routes an OpenAI Decisions request by provider type. If
// req.ProviderName is set, routing prefers the named provider instance over
// the first registered provider of the given type.
func (r *Router) Decisions(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if providerName := requestedProviderName(req); providerName != "" {
		if dp, ok := r.providerByNameRegistry(providerName).(core.DecisionsProvider); ok {
			return dp.Decisions(ctx, req)
		}
	}
	provider, err := r.resolveProviderType(providerType)
	dp, err := assertProviderCapability[core.DecisionsProvider](provider, err, func() error {
		return core.NewInvalidRequestError(fmt.Sprintf("%s does not support the decisions API", providerType), nil)
	})
	if err != nil {
		return nil, err
	}
	return dp.Decisions(ctx, req)
}

// DecisionsReadImages reports whether the named provider instance can answer
// a Decisions request with images. known is false when the provider does not
// say, which callers treat as reading them.
func (r *Router) DecisionsReadImages(providerName string) (readsImages, known bool) {
	reader, ok := r.providerByNameRegistry(strings.TrimSpace(providerName)).(core.DecisionImageReader)
	if !ok {
		return false, false
	}
	return reader.DecisionsReadImages(), true
}

// DecisionsProviderTypes returns the registered provider types that answer
// OpenAI Decisions requests.
func (r *Router) DecisionsProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.DecisionsProvider)
		return ok
	})
}
