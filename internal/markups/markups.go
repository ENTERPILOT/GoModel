// Package markups stamps the billed cost onto usage entries: the provider cost
// plus the pricing.markups percentage of the most specific matching selector.
package markups

import (
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/usage"
)

// ErrCustomerMarkupsUnavailable reports pricing.user_path_markups configured
// without the customer_markups capability. Startup fails rather than billing
// customers at the global rate.
var ErrCustomerMarkupsUnavailable = errors.New("pricing.user_path_markups requires a GoModel Pro license with the billing entitlement")

type rule struct {
	selector string
	percent  float64
}

type exactKey struct {
	providerName string
	model        string
}

// ruleSet holds one set of markups, indexed by selector kind.
type ruleSet struct {
	global       *rule
	providerWide map[string]rule
	modelWide    map[string]rule
	exact        map[exactKey]rule
}

// Service resolves markups from config. It is immutable after New, so it is
// safe for concurrent use.
type Service struct {
	billCacheHits bool
	rules         ruleSet
	// userPaths maps a canonical user path (never "/") to its own markups.
	userPaths map[string]*ruleSet
}

// New builds the markup service, validating each selector against the
// configured provider names. customerMarkups reports whether the
// customer_markups capability is enabled; without it, configured
// pricing.user_path_markups fail with ErrCustomerMarkupsUnavailable.
func New(cfg config.PricingConfig, catalog modelselectors.Catalog, customerMarkups bool) (*Service, error) {
	providerNames := modelselectors.ProviderNames(catalog)
	rules, err := newRuleSet("pricing.markups", "", cfg.Markups, providerNames)
	if err != nil {
		return nil, err
	}
	s := &Service{billCacheHits: cfg.BillCacheHits, rules: *rules}
	if len(cfg.UserPathMarkups) == 0 {
		return s, nil
	}
	if !customerMarkups {
		return nil, ErrCustomerMarkupsUnavailable
	}

	s.userPaths = make(map[string]*ruleSet, len(cfg.UserPathMarkups))
	seen := make(map[string]string, len(cfg.UserPathMarkups))
	for raw, markups := range cfg.UserPathMarkups {
		userPath, err := core.NormalizeUserPath(raw)
		if err != nil {
			return nil, fmt.Errorf("pricing.user_path_markups[%q]: %w", raw, err)
		}
		if userPath == "" || userPath == "/" {
			return nil, fmt.Errorf("pricing.user_path_markups[%q]: the root user path is covered by pricing.markups", raw)
		}
		if previous, ok := seen[userPath]; ok {
			return nil, fmt.Errorf("pricing.user_path_markups[%q]: duplicates user path %q", raw, previous)
		}
		seen[userPath] = raw

		field := fmt.Sprintf("pricing.user_path_markups[%q]", raw)
		pathRules, err := newRuleSet(field, userPath+":", markups, providerNames)
		if err != nil {
			return nil, err
		}
		s.userPaths[userPath] = pathRules
	}
	return s, nil
}

// newRuleSet validates and indexes one markup map. labelPrefix is prepended to
// each recorded selector so a usage row names the rule it was billed at.
func newRuleSet(field, labelPrefix string, markups map[string]float64, providerNames []string) (*ruleSet, error) {
	rs := &ruleSet{
		providerWide: map[string]rule{},
		modelWide:    map[string]rule{},
		exact:        map[exactKey]rule{},
	}
	seen := make(map[string]string, len(markups))
	for raw, percent := range markups {
		sel, err := modelselectors.NormalizeInputWithProviderNames(providerNames, raw)
		if err != nil {
			return nil, fmt.Errorf("%s[%q]: %w", field, raw, err)
		}
		kind := modelselectors.ScopeKindFor(sel.Selector, sel.ProviderName, sel.Model)
		// "name/" reads as a model selector when name is not a configured
		// provider; reject it so a misspelled provider never goes unpriced.
		if strings.HasSuffix(strings.TrimSpace(raw), "/") && kind == modelselectors.ScopeModel {
			return nil, fmt.Errorf("%s[%q]: unknown provider name", field, raw)
		}
		if previous, ok := seen[sel.Selector]; ok {
			return nil, fmt.Errorf("%s[%q]: duplicates selector %q", field, raw, previous)
		}
		seen[sel.Selector] = raw

		r := rule{selector: labelPrefix + sel.Selector, percent: percent}
		switch kind {
		case modelselectors.ScopeGlobal:
			rs.global = &r
		case modelselectors.ScopeProvider:
			rs.providerWide[sel.ProviderName] = r
		case modelselectors.ScopeModel:
			rs.modelWide[sel.Model] = r
		case modelselectors.ScopeProviderModel:
			rs.exact[exactKey{providerName: sel.ProviderName, model: sel.Model}] = r
		}
	}
	return rs, nil
}

// Apply sets the entry's markup and billed cost. The provider cost fields are
// left untouched.
func (s *Service) Apply(entry *usage.UsageEntry) {
	if s == nil || entry == nil {
		return
	}
	entry.MarkupPercent = nil
	entry.MarkupSelector = ""
	if r, ok := s.match(entry.UserPath, entryProvider(entry), entry.RoutedModel, entry.Model); ok {
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

// match tries the markups of the deepest configured user path covering
// userPath, then each shallower one, then pricing.markups. The first set with
// a matching selector wins, so markups never stack.
func (s *Service) match(userPath, providerName string, models ...string) (rule, bool) {
	providerName = strings.TrimSpace(providerName)
	if len(s.userPaths) > 0 {
		for path := strings.TrimSpace(userPath); path != "" && path != "/"; path = parentUserPath(path) {
			if rs, ok := s.userPaths[path]; ok {
				if r, ok := rs.match(providerName, models); ok {
					return r, true
				}
			}
		}
	}
	return s.rules.match(providerName, models)
}

// match returns the most specific rule: provider/model, then model, then
// provider, then global — the same precedence as model pricing overrides.
// Model rules are tried for each model in order, so a rule for the routed
// model wins over one for the answering model, as in usage pricing.
func (rs *ruleSet) match(providerName string, models []string) (rule, bool) {
	for _, model := range models {
		model = trimProviderPrefix(strings.TrimSpace(model), providerName)
		if model == "" {
			continue
		}
		if providerName != "" {
			if r, ok := rs.exact[exactKey{providerName: providerName, model: model}]; ok {
				return r, true
			}
		}
		if r, ok := rs.modelWide[model]; ok {
			return r, true
		}
	}
	if r, ok := rs.providerWide[providerName]; ok && providerName != "" {
		return r, true
	}
	if rs.global != nil {
		return *rs.global, true
	}
	return rule{}, false
}

// trimProviderPrefix strips a leading "providerName/" from model without
// allocating, since match may run once per configured user path level.
func trimProviderPrefix(model, providerName string) string {
	n := len(providerName)
	if n > 0 && len(model) > n && model[n] == '/' && model[:n] == providerName {
		return model[n+1:]
	}
	return model
}

// parentUserPath returns the parent of a canonical user path: "/a/b" -> "/a",
// "/a" -> "/".
func parentUserPath(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "/"
}

// entryProvider is the configured provider instance that served the entry,
// falling back to the provider type for entries logged without one.
func entryProvider(entry *usage.UsageEntry) string {
	if name := strings.TrimSpace(entry.ProviderName); name != "" {
		return name
	}
	return entry.Provider
}
