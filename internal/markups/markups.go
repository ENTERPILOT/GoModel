// Package markups stamps the billed cost onto usage entries: the provider cost
// plus the pricing.markups percentage of the most specific matching selector.
package markups

import (
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/usage"
)

type rule struct {
	selector string
	percent  float64
}

// Service resolves markups from config. It is immutable after New, so it is
// safe for concurrent use.
type Service struct {
	billCacheHits bool
	global        *rule
	providerWide  map[string]rule
	modelWide     map[string]rule
	exact         map[string]rule
}

// New builds the markup service, validating each selector against the
// configured provider names.
func New(cfg config.PricingConfig, catalog modelselectors.Catalog) (*Service, error) {
	s := &Service{
		billCacheHits: cfg.BillCacheHits,
		providerWide:  map[string]rule{},
		modelWide:     map[string]rule{},
		exact:         map[string]rule{},
	}
	providerNames := modelselectors.ProviderNames(catalog)
	seen := make(map[string]string, len(cfg.Markups))
	for raw, percent := range cfg.Markups {
		sel, err := modelselectors.NormalizeInputWithProviderNames(providerNames, raw)
		if err != nil {
			return nil, fmt.Errorf("pricing.markups[%q]: %w", raw, err)
		}
		kind := modelselectors.ScopeKindFor(sel.Selector, sel.ProviderName, sel.Model)
		// "name/" reads as a model selector when name is not a configured
		// provider; reject it so a misspelled provider never goes unpriced.
		if strings.HasSuffix(strings.TrimSpace(raw), "/") && kind == modelselectors.ScopeModel {
			return nil, fmt.Errorf("pricing.markups[%q]: unknown provider name", raw)
		}
		if previous, ok := seen[sel.Selector]; ok {
			return nil, fmt.Errorf("pricing.markups[%q]: duplicates selector %q", raw, previous)
		}
		seen[sel.Selector] = raw

		r := rule{selector: sel.Selector, percent: percent}
		switch kind {
		case modelselectors.ScopeGlobal:
			s.global = &r
		case modelselectors.ScopeProvider:
			s.providerWide[sel.ProviderName] = r
		case modelselectors.ScopeModel:
			s.modelWide[sel.Model] = r
		case modelselectors.ScopeProviderModel:
			s.exact[modelselectors.ExactMatchKey(sel.ProviderName, sel.Model)] = r
		}
	}
	return s, nil
}

// Apply sets the entry's markup and billed cost. The provider cost fields are
// left untouched.
func (s *Service) Apply(entry *usage.UsageEntry) {
	if s == nil || entry == nil {
		return
	}
	entry.MarkupPercent = nil
	entry.MarkupSelector = ""
	if r, ok := s.match(entryProvider(entry), entry.Model); ok {
		percent := r.percent
		entry.MarkupPercent = &percent
		entry.MarkupSelector = r.selector
	}
	entry.BilledCost = usage.BilledCost(entry.TotalCost, entry.MarkupPercent, entry.CacheType != "", s.billCacheHits)
}

// BillCacheHits reports whether response-cache hits are billed.
func (s *Service) BillCacheHits() bool {
	return s != nil && s.billCacheHits
}

// match returns the most specific rule: provider/model, then model, then
// provider, then global — the same precedence as model pricing overrides.
func (s *Service) match(providerName, model string) (rule, bool) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName != "" {
		model = strings.TrimPrefix(model, providerName+"/")
	}
	if r, ok := s.exact[modelselectors.ExactMatchKey(providerName, model)]; ok {
		return r, true
	}
	if r, ok := s.modelWide[model]; ok && model != "" {
		return r, true
	}
	if r, ok := s.providerWide[providerName]; ok && providerName != "" {
		return r, true
	}
	if s.global != nil {
		return *s.global, true
	}
	return rule{}, false
}

// entryProvider is the configured provider instance that served the entry,
// falling back to the provider type for entries logged without one.
func entryProvider(entry *usage.UsageEntry) string {
	if name := strings.TrimSpace(entry.ProviderName); name != "" {
		return name
	}
	return entry.Provider
}
