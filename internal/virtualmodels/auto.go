package virtualmodels

import (
	"fmt"
	"sort"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
)

// routeLevelNames lists the route levels, lowest first.
var routeLevelNames = core.RouteLevels()

// levelPool narrows targets to those of one level for the auto strategy: the
// lowest level at or above required, or — when no target reaches it — the
// highest level present, so a request is served by a weaker model rather
// than refused. required is a route level rank; zero means the request named
// no level and gets the highest one. Declared order is kept. It returns
// targets unchanged when it is empty.
func levelPool(targets []resolvedTarget, required int) []resolvedTarget {
	if len(targets) == 0 {
		return targets
	}
	chosen := chosenLevel(targets, required)
	out := make([]resolvedTarget, 0, len(targets))
	for _, target := range targets {
		if target.level == chosen {
			out = append(out, target)
		}
	}
	return out
}

// chosenLevel is the level levelPool serves required with.
func chosenLevel(targets []resolvedTarget, required int) int {
	highest, lowestCovering := 0, 0
	for _, target := range targets {
		highest = max(highest, target.level)
		if required > 0 && target.level >= required && (lowestCovering == 0 || target.level < lowestCovering) {
			lowestCovering = target.level
		}
	}
	if lowestCovering > 0 {
		return lowestCovering
	}
	return highest
}

// failoverOrder returns the direct targets of entry in the order a request
// fails over through them. Under every strategy but auto that is the
// declared order. Under auto it is the level the request was served at
// first, then the higher levels going up, then the lower ones going down, so
// a retry reaches a weaker model only after every model at least as capable
// has failed. level is the rank the route chose for the request; zero, when
// the caller does not know it, counts as the highest level present.
func (s *snapshot) failoverOrder(entry *redirectEntry, level int) []resolvedTarget {
	if normalizeStrategy(entry.strategy) != StrategyAuto {
		return entry.targets
	}
	if level == 0 {
		level = chosenLevel(entry.targets, 0)
	}
	// distance orders the levels: the served one, those above it going up,
	// then those below it going down.
	distance := func(target resolvedTarget) int {
		if target.level >= level {
			return target.level - level
		}
		return len(routeLevelNames) + level - target.level
	}
	ordered := append([]resolvedTarget(nil), entry.targets...)
	sort.SliceStable(ordered, func(i, j int) bool { return distance(ordered[i]) < distance(ordered[j]) })
	return ordered
}

// failoverLeaves flattens entry into the concrete, currently-available models
// a request fails over through, in order (see failoverOrder), descending
// chained virtual models.
func (s *snapshot) failoverLeaves(entry *redirectEntry, level int, catalog Catalog) []resolvedTarget {
	if catalog == nil {
		return nil
	}
	out := make([]resolvedTarget, 0, len(entry.targets))
	for _, target := range s.failoverOrder(entry, level) {
		inner, ok := s.chained(entry.vm.Source, target)
		if !ok {
			if modelServable(catalog, target.qualified) {
				out = append(out, target)
			}
			continue
		}
		// A chain holds at most one auto route (see validateAutoRoutes), so
		// the level belongs to whichever redirect on the way uses it.
		if inner.vm.Enabled {
			out = append(out, s.failoverLeaves(inner, level, catalog)...)
		}
	}
	return out
}

// validateAutoRoutes rejects a virtual model using the auto strategy that
// routes into another one. A request names one level, so two auto routes in
// one chain would both spend it, and the inner route's levels would be
// hidden behind the single level the outer route gave its target.
func validateAutoRoutes(s *snapshot) error {
	for _, source := range s.order {
		entry := s.redirects[source]
		if normalizeStrategy(entry.strategy) != StrategyAuto {
			continue
		}
		if inner, ok := s.autoRouteBehind(entry); ok {
			return newValidationError(fmt.Sprintf(
				"virtual model %q uses the %q strategy and routes into %q, which uses it too; give only one of them the strategy",
				source, StrategyAuto, inner), nil)
		}
	}
	return nil
}

// autoRouteBehind returns the source of an auto route reachable through the
// chained targets of entry. Chains are acyclic by construction (see
// validateChains), so this terminates.
func (s *snapshot) autoRouteBehind(entry *redirectEntry) (string, bool) {
	for _, target := range entry.targets {
		inner, ok := s.chained(entry.vm.Source, target)
		if !ok {
			continue
		}
		if normalizeStrategy(inner.strategy) == StrategyAuto {
			return inner.vm.Source, true
		}
		if source, ok := s.autoRouteBehind(inner); ok {
			return source, true
		}
	}
	return "", false
}

// AutoRoute implements ext.AutoRoutes: it returns the auto route a request
// for model by userPath resolves to, following single-target aliases. A
// balanced redirect in front of an auto route is not followed, since which
// of its targets serves is not known before the request is routed.
func (s *Service) AutoRoute(model, userPath string) (ext.AutoRoute, bool) {
	if s == nil {
		return ext.AutoRoute{}, false
	}
	snap := s.snapshot()
	entry, ok := snap.findRedirect(model, userPath, true)
	for ok && normalizeStrategy(entry.strategy) != StrategyAuto {
		if len(entry.targets) != 1 {
			return ext.AutoRoute{}, false
		}
		// Chains are acyclic and bounded by construction (see validateChains).
		if entry, ok = snap.chained(entry.vm.Source, entry.targets[0]); ok && !entry.vm.Enabled {
			return ext.AutoRoute{}, false
		}
	}
	if !ok {
		return ext.AutoRoute{}, false
	}
	route := ext.AutoRoute{Source: entry.vm.Source}
	for i, level := range routeLevelNames {
		for _, target := range entry.targets {
			if target.level == i+1 {
				route.Levels = append(route.Levels, level)
				break
			}
		}
	}
	return route, true
}
