package pricingoverrides

import (
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResolvePricing resolves base pricing and applies the most specific DB override.
func (s *Service) ResolvePricing(model, providerName string) *core.ModelPricing {
	if s == nil {
		return nil
	}
	providerName = strings.TrimSpace(providerName)
	rawModel := strings.TrimSpace(model)
	model = modelIDFromSelector(rawModel, providerName)

	var basePricing *core.ModelPricing
	if s.base != nil {
		basePricing = s.base.ResolvePricing(model, providerName)
		if basePricing == nil && rawModel != "" && rawModel != model {
			basePricing = s.base.ResolvePricing(rawModel, providerName)
		}
	}

	if rule, ok := s.snapshot().matchingOverride(providerName, model); ok {
		return mergePricing(basePricing, rule.override.Pricing)
	}
	return cloneBasePricing(basePricing)
}

// HasModelPricing reports whether pricing is declared for exactly this model:
// catalog pricing, or an override scoped to the model rather than to its
// provider or to every model.
func (s *Service) HasModelPricing(model, providerName string) bool {
	if s == nil {
		return false
	}
	providerName = strings.TrimSpace(providerName)
	rawModel := strings.TrimSpace(model)
	model = modelIDFromSelector(rawModel, providerName)
	if model == "" {
		return false
	}
	if s.snapshot().hasModelScopedOverride(providerName, model) {
		return true
	}
	if s.base == nil {
		return false
	}
	return s.base.ResolvePricing(model, providerName) != nil ||
		(rawModel != model && s.base.ResolvePricing(rawModel, providerName) != nil)
}

func cloneBasePricing(base *core.ModelPricing) *core.ModelPricing {
	if base == nil {
		return nil
	}
	cloned := base.Clone()
	if cloned != nil && strings.TrimSpace(cloned.Currency) == "" {
		cloned.Currency = CurrencyUSD
	}
	return cloned
}

func modelIDFromSelector(model, providerName string) string {
	model = strings.TrimSpace(model)
	providerName = strings.TrimSpace(providerName)
	if providerName != "" && strings.HasPrefix(model, providerName+"/") {
		return strings.TrimSpace(strings.TrimPrefix(model, providerName+"/"))
	}
	return model
}
