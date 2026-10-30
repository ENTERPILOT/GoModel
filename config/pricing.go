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
	// MarkupsEnabled turns on markups and billed cost: usage rows record a
	// billed cost and the dashboard shows it. While off, Markups and
	// BillCacheHits are ignored and no billed cost is recorded.
	// Default: false
	MarkupsEnabled bool `yaml:"markups_enabled" env:"PRICING_MARKUPS_ENABLED"`

	// Markups maps a model selector to a percentage added on top of the
	// provider cost: "/" (all models), "openai/" (one provider instance),
	// "gpt-5" (a model on any provider) or "openai/gpt-5". The most specific
	// selector wins; markups never stack. 20 bills $1.20 for $1.00 of cost.
	// Markups are declared in config.yaml only.
	// Default: none (billed cost equals provider cost).
	Markups map[string]float64 `yaml:"markups"`

	// UserPathMarkups gives a customer (a user-path subtree) its own markups,
	// keyed by user path and then by the same selectors as Markups. The
	// deepest configured path covering the request's user path that has a
	// matching selector wins; otherwise shallower paths, then Markups, apply.
	// Requires GoModel Pro (the customer_markups capability).
	// Markups are declared in config.yaml only.
	// Default: none.
	UserPathMarkups map[string]map[string]float64 `yaml:"user_path_markups"`

	// BillCacheHits controls whether responses served from the gateway
	// response cache are billed at what the same call would cost when the hit
	// is served, plus markup. False bills them at zero. Provider prompt caching
	// is unaffected: its cached-token cost is always billed.
	// Default: true
	BillCacheHits bool `yaml:"bill_cache_hits" env:"PRICING_BILL_CACHE_HITS"`
}

// validatePricingConfig checks markup values. Selectors and user paths are
// validated against the configured providers when the markup service is built.
func validatePricingConfig(cfg *PricingConfig) error {
	if cfg == nil {
		return nil
	}
	if err := validateMarkupValues("pricing.markups", cfg.Markups); err != nil {
		return err
	}
	for userPath, markups := range cfg.UserPathMarkups {
		if strings.TrimSpace(userPath) == "" {
			return fmt.Errorf("pricing.user_path_markups: user path is required")
		}
		if err := validateMarkupValues(fmt.Sprintf("pricing.user_path_markups[%q]", userPath), markups); err != nil {
			return err
		}
	}
	return nil
}

func validateMarkupValues(field string, markups map[string]float64) error {
	for selector, percent := range markups {
		if strings.TrimSpace(selector) == "" {
			return fmt.Errorf("%s: selector is required", field)
		}
		if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 {
			return fmt.Errorf("%s[%q] must be a finite number >= 0", field, selector)
		}
	}
	return nil
}
