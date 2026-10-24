package usage

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// PricingResolver resolves pricing metadata for a given model and provider type.
// Implementations should check the registry first and fall back to a reverse-index
// lookup when the model ID in the usage DB differs from the registry key.
type PricingResolver interface {
	ResolvePricing(model, providerType string) *core.ModelPricing
}

// ModelPricingChecker is implemented by pricing resolvers that can tell
// pricing declared for exactly one model (catalog pricing or a model-scoped
// override) from a broad provider-wide or global rule.
type ModelPricingChecker interface {
	HasModelPricing(model, providerType string) bool
}

// ResolveServedModelPricing prices a response routed to one model and
// answered by another, such as the alias jev-latest answered by jev-1.13.0.
// Pricing declared for exactly the routed model wins, then pricing declared
// for exactly the answering model, then any broader rule matching the routed
// model, then the answering model. answered is called at most once, and only
// when the routed model has no exact pricing.
func ResolveServedModelPricing(resolver PricingResolver, routed, providerType string, answered func() string) *core.ModelPricing {
	if resolver == nil {
		return nil
	}
	routed = strings.TrimSpace(routed)
	checker, canCheck := resolver.(ModelPricingChecker)
	if canCheck && checker.HasModelPricing(routed, providerType) {
		return resolver.ResolvePricing(routed, providerType)
	}

	answeredModel, resolved := "", false
	answeredOnce := func() string {
		if !resolved && answered != nil {
			if model := strings.TrimSpace(answered()); model != routed {
				answeredModel = model
			}
		}
		resolved = true
		return answeredModel
	}
	if canCheck {
		if model := answeredOnce(); model != "" && checker.HasModelPricing(model, providerType) {
			return resolver.ResolvePricing(model, providerType)
		}
	}
	if pricing := resolver.ResolvePricing(routed, providerType); pricing != nil {
		return pricing
	}
	if model := answeredOnce(); model != "" {
		return resolver.ResolvePricing(model, providerType)
	}
	return nil
}
