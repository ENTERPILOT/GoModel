package virtualmodels

import (
	"sync"
	"sync/atomic"

	"github.com/enterpilot/gomodel/internal/core"
)

// roundRobin holds a monotonic request counter per redirect source. It lives on
// the Service (not the snapshot) so load-balancing position survives the periodic
// snapshot swaps that reload virtual models from storage.
type roundRobin struct {
	counters sync.Map // source -> *atomic.Uint64
}

// next returns the current counter for source and advances it. The first call for
// a source returns 0.
func (r *roundRobin) next(source string) uint64 {
	value, _ := r.counters.LoadOrStore(source, new(atomic.Uint64))
	return value.(*atomic.Uint64).Add(1) - 1
}

// prune removes counters for redirect sources no longer present in the latest
// snapshot, preventing long-lived processes from retaining deleted aliases.
func (r *roundRobin) prune(active map[string]*redirectEntry) {
	r.counters.Range(func(key, _ any) bool {
		source, ok := key.(string)
		if !ok {
			r.counters.Delete(key)
			return true
		}
		if _, exists := active[source]; !exists {
			r.counters.Delete(key)
		}
		return true
	})
}

// balancedResolution chooses one concrete model for a request through entry,
// applying its load-balancing strategy across the targets currently viable. A
// target that names another virtual model is one leg of the strategy like any
// concrete target; once chosen it resolves through its own strategy, so each
// level of a chain balances independently. When the request carries a session
// id and the redirect keeps session affinity (the default), the target that
// served the session before is preferred while it stays viable; otherwise the
// strategy picks and the choice is re-pinned. It reports false when no target
// is available.
func (s *Service) balancedResolution(snap *snapshot, entry *redirectEntry, sessionID string) (core.ModelSelector, bool) {
	supported := snap.viableTargets(entry, s.catalog)
	if len(supported) == 0 {
		return core.ModelSelector{}, false
	}
	// Prefer targets with live rate-limit capacity. When every live target is
	// saturated, fall back to the first declared one: the request then reaches
	// admission and receives an honest 429 with Retry-After (or defers to
	// failover) instead of the all-targets-down error path.
	pool := s.targetsWithCapacity(snap, entry, supported)
	if len(pool) == 0 {
		// This target is selected only to reach admission and produce the 429.
		// Do not run affinity resolution: a transient capacity burst must not
		// discard or replace the target that actually served the session.
		return s.concreteTarget(snap, entry, supported[0], sessionID)
	}

	// pick applies the redirect's strategy to the viable pool. A single viable
	// target needs no strategy and must not advance round-robin state, so an
	// alias and a one-target-available redirect behave identically.
	pick := func() resolvedTarget {
		if len(pool) == 1 {
			return pool[0]
		}
		switch normalizeStrategy(entry.strategy) {
		case StrategyFailover:
			// Declared order is priority order: the first viable leg is the
			// primary; the legs below it are the failover chain.
			return pool[0]
		case StrategyCost:
			return s.cheapestTarget(snap, entry, pool)
		case StrategyAdaptive:
			if target, ok := s.adaptiveTarget(entry, sessionID, pool); ok {
				return target
			}
			return pool[weightedIndex(pool, s.balancer.next(entry.vm.Source))]
		default: // StrategyRoundRobin
			return pool[weightedIndex(pool, s.balancer.next(entry.vm.Source))]
		}
	}
	// Affinity is keyed to the redirect's CONFIGURED shape, not the targets
	// currently available: with only one target momentarily supported (provider
	// outage, startup) the session must still pin its serving target, or the
	// strategy could move an active conversation once the others come back.
	// Failover is deterministic (always the primary), so it never pins.
	affinity := sessionID != "" && entry.sessionAffinity() && len(entry.targets) > 1 &&
		normalizeStrategy(entry.strategy) != StrategyFailover
	if affinity {
		viable := func(candidate string) bool {
			_, ok := poolTarget(pool, candidate)
			return ok
		}
		if qualified, ok := s.sticky.lookup(entry.vm.Source, sessionID, viable); ok {
			if target, found := poolTarget(pool, qualified); found {
				return s.concreteTarget(snap, entry, target, sessionID)
			}
		}

		// Strategy selection may read the model catalog, so keep it outside the
		// sticky lock. resolve rechecks the pin atomically in case another first
		// request selected and pinned a target concurrently.
		choice := pick()
		qualified := s.sticky.resolve(entry.vm.Source, sessionID,
			viable,
			choice.qualified,
		)
		if target, ok := poolTarget(pool, qualified); ok {
			return s.concreteTarget(snap, entry, target, sessionID)
		}
		return s.concreteTarget(snap, entry, choice, sessionID)
	}
	return s.concreteTarget(snap, entry, pick(), sessionID)
}

// concreteTarget turns a chosen target of entry into the concrete model to
// execute: the target itself, or — when it names another virtual model — that
// redirect's own balanced resolution. Chains are acyclic and bounded by
// construction (see validateChains), so the recursion terminates.
func (s *Service) concreteTarget(snap *snapshot, entry *redirectEntry, target resolvedTarget, sessionID string) (core.ModelSelector, bool) {
	inner, ok := snap.chained(entry.vm.Source, target)
	if !ok {
		return target.selector, true
	}
	if !inner.vm.Enabled {
		return core.ModelSelector{}, false
	}
	return s.balancedResolution(snap, inner, sessionID)
}

// poolTarget finds a qualified model among the viable targets.
func poolTarget(pool []resolvedTarget, qualified string) (resolvedTarget, bool) {
	for _, target := range pool {
		if target.qualified == qualified {
			return target, true
		}
	}
	return resolvedTarget{}, false
}

// targetsWithCapacity filters targets through the optional rate-limit capacity
// probe. Without a probe every target has capacity. A chained target has
// capacity while any concrete model behind it does.
func (s *Service) targetsWithCapacity(snap *snapshot, entry *redirectEntry, targets []resolvedTarget) []resolvedTarget {
	if s.targetCapacity == nil {
		return targets
	}
	out := make([]resolvedTarget, 0, len(targets))
	for _, target := range targets {
		for _, leaf := range snap.leaves(entry, target, s.catalog) {
			if s.targetCapacity(leaf.qualified) {
				out = append(out, target)
				break
			}
		}
	}
	return out
}

// weightedIndex maps a monotonic counter to a target index, honoring per-target
// weight. When every weight is 1 (or unset) it is a plain rotation; otherwise a
// target with weight w claims w consecutive slots of every sum(weights).
func weightedIndex(targets []resolvedTarget, counter uint64) int {
	total := 0
	weighted := false
	for _, target := range targets {
		weight := normalizeWeight(target.weight)
		total += weight
		if weight != 1 {
			weighted = true
		}
	}
	if !weighted || total <= 0 {
		return int(counter % uint64(len(targets)))
	}
	slot := int(counter % uint64(total))
	for i, target := range targets {
		slot -= normalizeWeight(target.weight)
		if slot < 0 {
			return i
		}
	}
	return len(targets) - 1
}

// normalizeWeight rounds a target weight to a positive integer share. A
// non-positive or unset weight counts as 1.
func normalizeWeight(weight float64) int {
	if weight <= 0 {
		return 1
	}
	rounded := int(weight + 0.5)
	if rounded < 1 {
		return 1
	}
	return rounded
}

// cheapestTarget returns the supported target with the lowest per-token price.
// Targets with no registry pricing are skipped while any priced target exists;
// when none are priced it falls back to the first supported target so the cost
// strategy stays deterministic. Ties keep the earlier target in support order.
// A chained target is priced at the cheapest concrete model behind it.
func (s *Service) cheapestTarget(snap *snapshot, entry *redirectEntry, supported []resolvedTarget) resolvedTarget {
	best := supported[0]
	bestCost, bestPriced := s.legCost(snap, entry, best)
	for _, target := range supported[1:] {
		cost, priced := s.legCost(snap, entry, target)
		if !priced {
			continue
		}
		if !bestPriced || cost < bestCost {
			best, bestCost, bestPriced = target, cost, true
		}
	}
	return best
}

// legCost prices one leg of a strategy: a concrete target's own price, or the
// lowest price among the available concrete models behind a chained target.
func (s *Service) legCost(snap *snapshot, entry *redirectEntry, target resolvedTarget) (float64, bool) {
	if _, ok := snap.chained(entry.vm.Source, target); !ok {
		return s.targetCost(target)
	}
	bestCost, priced := 0.0, false
	for _, leaf := range snap.leaves(entry, target, s.catalog) {
		if cost, ok := s.targetCost(leaf); ok && (!priced || cost < bestCost) {
			bestCost, priced = cost, true
		}
	}
	return bestCost, priced
}

// targetCost returns a comparable per-token price for a concrete target — the sum of its
// input and output per-million-token rates — and whether the registry priced it.
func (s *Service) targetCost(target resolvedTarget) (float64, bool) {
	model, ok := s.catalog.LookupModel(target.qualified)
	if !ok || model == nil || model.Metadata == nil || model.Metadata.Pricing == nil {
		return 0, false
	}
	pricing := model.Metadata.Pricing
	if pricing.InputPerMtok == nil && pricing.OutputPerMtok == nil {
		return 0, false
	}
	cost := 0.0
	if pricing.InputPerMtok != nil {
		cost += *pricing.InputPerMtok
	}
	if pricing.OutputPerMtok != nil {
		cost += *pricing.OutputPerMtok
	}
	return cost, true
}
