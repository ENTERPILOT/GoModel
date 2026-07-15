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
func (r *roundRobin) prune(active map[string]redirectEntry) {
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

// balancedResolution chooses one concrete target for a request through entry,
// applying its load-balancing strategy across the targets the catalog currently
// supports. It reports false when no target is available.
func (s *Service) balancedResolution(entry redirectEntry) (core.ModelSelector, bool) {
	supported := entry.supportedTargets(s.catalog)
	if len(supported) == 0 {
		return core.ModelSelector{}, false
	}
	// Prefer targets with live rate-limit capacity. When every live target is
	// saturated, fall back to the first declared one: the request then reaches
	// admission and receives an honest 429 with Retry-After (or defers to
	// failover) instead of the all-targets-down error path.
	pool := s.targetsWithCapacity(supported)
	if len(pool) == 0 {
		pool = supported[:1]
	}
	if len(pool) == 1 {
		// A single viable target needs no strategy and must not advance
		// round-robin state, so an alias and a one-target-available redirect
		// behave identically.
		return pool[0].selector, true
	}

	switch normalizeStrategy(entry.strategy) {
	case StrategyCost:
		return s.cheapestTarget(pool).selector, true
	default: // StrategyRoundRobin
		index := weightedIndex(pool, s.balancer.next(entry.vm.Source))
		return pool[index].selector, true
	}
}

// targetsWithCapacity filters targets through the optional rate-limit capacity
// probe. Without a probe every target has capacity.
func (s *Service) targetsWithCapacity(targets []resolvedTarget) []resolvedTarget {
	if s.targetCapacity == nil {
		return targets
	}
	out := make([]resolvedTarget, 0, len(targets))
	for _, target := range targets {
		if s.targetCapacity(target.qualified) {
			out = append(out, target)
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
func (s *Service) cheapestTarget(supported []resolvedTarget) resolvedTarget {
	best := supported[0]
	bestCost, bestPriced := s.targetCost(best)
	for _, target := range supported[1:] {
		cost, priced := s.targetCost(target)
		if !priced {
			continue
		}
		if !bestPriced || cost < bestCost {
			best, bestCost, bestPriced = target, cost, true
		}
	}
	return best
}

// targetCost returns a comparable per-token price for a target — the sum of its
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
