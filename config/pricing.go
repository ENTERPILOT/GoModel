package config

import (
	"fmt"
	"math"
	"strings"
)

// PricingConfig holds how tracked provider cost is turned into the amount
// billed to the caller. It never changes the provider cost itself, so budgets
// and cost reports keep measuring upstream spend.
type PricingConfig struct {
	// Markups maps a model selector to a percentage added on top of the
	// provider cost: "/" (all models), "openai/" (one provider instance),
	// "gpt-5" (a model on any provider) or "openai/gpt-5". The most specific
	// selector wins; markups never stack. 20 bills $1.20 for $1.00 of cost.
	// Markups are declared in config.yaml only.
	// Default: none (billed cost equals provider cost).
	Markups map[string]float64 `yaml:"markups"`

	// BillCacheHits controls whether responses served from the gateway
	// response cache are billed at the price of the original call plus
	// markup. False bills them at zero. Provider prompt caching is unaffected:
	// its cached-token cost is always billed.
	// Default: true
	BillCacheHits bool `yaml:"bill_cache_hits" env:"PRICING_BILL_CACHE_HITS"`
}

// validatePricingConfig checks markup values. Selectors are validated against
// the configured providers when the markup service is built.
func validatePricingConfig(cfg *PricingConfig) error {
	if cfg == nil {
		return nil
	}
	for selector, percent := range cfg.Markups {
		if strings.TrimSpace(selector) == "" {
			return fmt.Errorf("pricing.markups: selector is required")
		}
		if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 {
			return fmt.Errorf("pricing.markups[%q] must be a finite number >= 0", selector)
		}
	}
	return nil
}
