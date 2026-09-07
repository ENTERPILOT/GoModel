package virtualmodels

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
)

// resolvedTarget is one redirect destination with its selector parsed at build
// time so resolution avoids re-parsing on every request.
type resolvedTarget struct {
	selector  core.ModelSelector
	qualified string
	weight    float64
	// explicitProvider records that the declaration named a provider. A
	// bare "a/b" model may be a slash-shaped model id or a slash-named virtual
	// model, so only an explicit provider pins the target to a concrete model.
	explicitProvider bool
}

// redirectEntry is a redirect row plus its parsed targets and strategy,
// precomputed at build time so resolution avoids re-parsing on every request.
type redirectEntry struct {
	vm       VirtualModel
	targets  []resolvedTarget
	strategy string
	// route caches the validated strategy_config handed to a routing-strategy
	// plugin; nil for entries built outside buildSnapshot.
	route *routeConfigCache
}

// sessionAffinity reports whether this redirect keeps sessions pinned to the
// target that served them. Enabled unless explicitly disabled.
func (e redirectEntry) sessionAffinity() bool {
	return e.vm.SessionAffinity == nil || *e.vm.SessionAffinity
}

// failover reports whether a request that fails on the chosen target is
// retried on the redirect's other targets. Enabled unless explicitly disabled;
// the failover strategy is a priority list and always fails over.
func (e redirectEntry) failover() bool {
	return e.vm.Failover == nil || *e.vm.Failover || normalizeStrategy(e.strategy) == StrategyFailover
}

// shadowsSource reports whether the redirect lists its own source among its
// targets: it then covers the concrete model of that name (adding failover or
// balancing to it) instead of replacing it.
func (e *redirectEntry) shadowsSource() bool {
	for _, target := range e.targets {
		if target.qualified == e.vm.Source {
			return true
		}
	}
	return false
}

// snapshot is the immutable in-memory projection of all virtual models. It
// indexes redirect rows by source and policy rows by scope, and keeps every row
// in bySource for Get and admin listing.
type snapshot struct {
	// redirects holds redirect rows keyed by trimmed Source, plus sorted order.
	redirects map[string]*redirectEntry
	order     []string

	// bySource holds every row (redirect and policy) keyed by Source.
	bySource map[string]VirtualModel

	// Policy scope indexes (policy rows only).
	global        VirtualModel
	hasGlobal     bool
	exact         map[string]VirtualModel
	providerWide  map[string]VirtualModel
	modelWide     map[string]VirtualModel
	defaultEnable bool
}

func emptySnapshot(defaultEnable bool) snapshot {
	return snapshot{
		redirects:     map[string]*redirectEntry{},
		order:         []string{},
		bySource:      map[string]VirtualModel{},
		exact:         map[string]VirtualModel{},
		providerWide:  map[string]VirtualModel{},
		modelWide:     map[string]VirtualModel{},
		defaultEnable: defaultEnable,
	}
}

// buildSnapshot normalizes and indexes all rows. It returns an error when a row
// fails normalization, which lets Upsert/Delete validate a candidate set before
// committing it.
func buildSnapshot(rows []VirtualModel, defaultEnable bool) (snapshot, error) {
	next := emptySnapshot(defaultEnable)
	next.redirects = make(map[string]*redirectEntry, len(rows))
	next.order = make([]string, 0, len(rows))
	next.bySource = make(map[string]VirtualModel, len(rows))

	for _, row := range rows {
		if row.IsRedirect() {
			normalized, selectors, err := normalizeRedirect(row)
			if err != nil {
				return snapshot{}, fmt.Errorf("load virtual model %q: %w", row.Source, err)
			}
			targets := make([]resolvedTarget, len(selectors))
			for i, selector := range selectors {
				targets[i] = resolvedTarget{
					selector:         selector,
					qualified:        selector.QualifiedModel(),
					weight:           normalized.Targets[i].Weight,
					explicitProvider: normalized.Targets[i].Provider != "",
				}
			}
			next.redirects[normalized.Source] = &redirectEntry{
				vm:       normalized,
				targets:  targets,
				strategy: normalized.Strategy,
				route:    &routeConfigCache{},
			}
			next.order = append(next.order, normalized.Source)
			next.bySource[normalized.Source] = normalized
			continue
		}

		normalized, err := normalizeStoredPolicy(row)
		if err != nil {
			return snapshot{}, fmt.Errorf("load virtual model %q: %w", row.Source, err)
		}
		next.bySource[normalized.Source] = normalized
		switch scopeKindFor(normalized.Source, normalized.ProviderName, normalized.Model) {
		case modelselectors.ScopeGlobal:
			next.global = normalized
			next.hasGlobal = true
		case modelselectors.ScopeProviderModel:
			next.exact[modelselectors.ExactMatchKey(normalized.ProviderName, normalized.Model)] = normalized
		case modelselectors.ScopeProvider:
			next.providerWide[normalized.ProviderName] = normalized
		default:
			next.modelWide[normalized.Model] = normalized
		}
	}
	sort.Strings(next.order)
	if err := validateChains(&next); err != nil {
		return snapshot{}, err
	}
	return next, nil
}

// rows returns a deep copy of every stored row, sorted by source. It is the
// basis for the candidate-snapshot validation in Upsert/Delete.
func (s *snapshot) rows() []VirtualModel {
	sources := make([]string, 0, len(s.bySource))
	for source := range s.bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	result := make([]VirtualModel, 0, len(sources))
	for _, source := range sources {
		result = append(result, s.bySource[source].clone())
	}
	return result
}

// lookupCanonicalSource finds a row by source, accepting an unnormalized policy
// selector (e.g. a raw model ID) by normalizing before giving up. It returns the
// row, its canonical source key, and whether it was found.
func (s *snapshot) lookupCanonicalSource(source string) (VirtualModel, string, bool) {
	source = strings.TrimSpace(source)
	if vm, ok := s.bySource[source]; ok {
		return vm, source, true
	}
	if parts, err := normalizeStoredPolicy(VirtualModel{Source: source}); err == nil {
		if vm, ok := s.bySource[parts.Source]; ok {
			return vm, parts.Source, true
		}
	}
	return VirtualModel{}, "", false
}

// findRedirect returns the redirect entry for a requested model name when it is
// enabled and, when enforced, the caller's user path is in scope. It performs no
// catalog lookup or target selection, so it never advances load-balancing state.
func (s *snapshot) findRedirect(name, userPath string, enforceUserPaths bool) (*redirectEntry, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	entry, ok := s.redirects[name]
	if !ok || !entry.vm.Enabled {
		return nil, false
	}
	if enforceUserPaths && len(entry.vm.UserPaths) > 0 && !userPathAllowed(userPath, entry.vm.UserPaths) {
		return nil, false
	}
	return entry, true
}

// resolveRedirect returns a stateless, representative resolution for a redirect
// name: the first catalog-supported concrete model behind it, descending chained
// virtual models. It backs validity checks and model listing, which must not
// advance any load-balancing state. The request path uses
// Service.balancedResolution, which applies the redirect's load-balancing
// strategy across all available targets.
func (s *snapshot) resolveRedirect(name string, catalog Catalog, userPath string, enforceUserPaths bool) (Resolution, bool) {
	name = strings.TrimSpace(name)
	resolution := Resolution{
		Requested: core.ModelSelector{Model: name},
		Resolved:  core.ModelSelector{Model: name},
	}
	entry, ok := s.findRedirect(name, userPath, enforceUserPaths)
	if !ok {
		return resolution, false
	}
	leaves := s.leafTargets(entry, catalog)
	if len(leaves) == 0 {
		return resolution, false
	}
	resolution.Resolved = leaves[0].selector
	resolution.Source = entry.vm.Source
	return resolution, true
}

// effectiveState resolves the compiled access state for one concrete selector.
func (s *snapshot) effectiveState(selector core.ModelSelector) EffectiveState {
	model := strings.TrimSpace(selector.Model)
	providerName := strings.TrimSpace(selector.Provider)
	state := EffectiveState{
		Selector:       selectorString(providerName, model),
		ProviderName:   providerName,
		Model:          model,
		DefaultEnabled: s.defaultEnable,
		Enabled:        s.defaultEnable,
	}
	if model == "" && providerName == "" {
		return state
	}

	if rule, ok := s.matchingPolicy(providerName, model); ok {
		// Native Enabled: a disabled policy row turns the model OFF; an enabled
		// row with user_paths restricts; an enabled row with no paths allows.
		state.Enabled = rule.Enabled
		state.UserPaths = append([]string(nil), rule.UserPaths...)
	}
	return state
}

// matchingPolicy returns the most specific policy row matching providerName and
// model: exact > providerWide > modelWide > global.
func (s *snapshot) matchingPolicy(providerName, model string) (VirtualModel, bool) {
	if key := modelselectors.ExactMatchKey(providerName, model); key != "" {
		if exact, ok := s.exact[key]; ok {
			return exact, true
		}
	}
	if providerName != "" {
		if providerWide, ok := s.providerWide[providerName]; ok {
			return providerWide, true
		}
	}
	if model != "" {
		if modelWide, ok := s.modelWide[model]; ok {
			return modelWide, true
		}
	}
	if s.hasGlobal {
		return s.global, true
	}
	return VirtualModel{}, false
}
