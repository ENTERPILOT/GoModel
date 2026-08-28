package virtualmodels

import (
	"fmt"
	"strings"
)

// MaxChainDepth caps how many virtual models a redirect may pass through
// before reaching a concrete model. A redirect whose targets are all concrete
// has depth 1; each virtual model hop adds one.
const MaxChainDepth = 8

// chained returns the redirect that a target of owner names, when it names
// one. A target without an explicit provider whose qualified name matches
// another redirect's source is a chain leg rather than a concrete model. A
// target naming owner itself is not a chain: it stands for the concrete model
// the redirect shadows. Disabled redirects are returned too: the caller
// decides whether that makes the leg unavailable.
func (s *snapshot) chained(owner string, target resolvedTarget) (*redirectEntry, bool) {
	if target.selector.Provider != "" || target.qualified == owner {
		return nil, false
	}
	entry, ok := s.redirects[target.qualified]
	return entry, ok
}

// viableTargets returns entry's direct targets that can currently serve a
// request, preserving declared order: a concrete target the catalog reports
// available, or a chained virtual model that is enabled and itself has a
// viable target. A provider whose latest model refresh failed keeps its models
// registered but is skipped here, so redirects route around it.
func (s *snapshot) viableTargets(entry *redirectEntry, catalog Catalog) []resolvedTarget {
	if catalog == nil {
		return nil
	}
	out := make([]resolvedTarget, 0, len(entry.targets))
	for _, target := range entry.targets {
		if s.viable(entry, target, catalog) {
			out = append(out, target)
		}
	}
	return out
}

// viable reports whether one target of owner can currently serve a request:
// a concrete model the catalog has available, or an enabled chained redirect
// with a viable target of its own. It is the allocation-free counterpart of
// leaves for the per-request path.
func (s *snapshot) viable(owner *redirectEntry, target resolvedTarget, catalog Catalog) bool {
	inner, ok := s.chained(owner.vm.Source, target)
	if !ok {
		return catalog.ModelAvailable(target.qualified)
	}
	if !inner.vm.Enabled {
		return false
	}
	for _, next := range inner.targets {
		if s.viable(inner, next, catalog) {
			return true
		}
	}
	return false
}

// leafTargets flattens entry into the concrete, currently-available models
// reachable through it, in declared order, descending chained virtual models.
// It backs the stateless projections (validity, model listing, admin view).
func (s *snapshot) leafTargets(entry *redirectEntry, catalog Catalog) []resolvedTarget {
	if catalog == nil {
		return nil
	}
	out := make([]resolvedTarget, 0, len(entry.targets))
	for _, target := range entry.targets {
		out = append(out, s.leaves(entry, target, catalog)...)
	}
	return out
}

// leaves returns the available concrete models behind one target of owner:
// the target itself when concrete, or the leaf targets of the enabled redirect
// it names. Chains are acyclic by construction (see validateChains), so this
// terminates.
func (s *snapshot) leaves(owner *redirectEntry, target resolvedTarget, catalog Catalog) []resolvedTarget {
	inner, ok := s.chained(owner.vm.Source, target)
	if !ok {
		if catalog.ModelAvailable(target.qualified) {
			return []resolvedTarget{target}
		}
		return nil
	}
	if !inner.vm.Enabled {
		return nil
	}
	return s.leafTargets(inner, catalog)
}

// representativeLeaf returns the first declared concrete model behind entry,
// descending enabled chains, independent of catalog availability. It gives
// callers a stable stand-in where no load-balancing state may advance. Chains
// are acyclic by construction (see validateChains), so this terminates.
func (s *snapshot) representativeLeaf(entry *redirectEntry) (resolvedTarget, bool) {
	for _, target := range entry.targets {
		inner, ok := s.chained(entry.vm.Source, target)
		if !ok {
			return target, true
		}
		if !inner.vm.Enabled {
			continue
		}
		if leaf, ok := s.representativeLeaf(inner); ok {
			return leaf, true
		}
	}
	return resolvedTarget{}, false
}

// dependents lists the redirects that chain through source, sorted by source.
func (s *snapshot) dependents(source string) []string {
	var out []string
	for _, name := range s.order {
		if name == source {
			continue
		}
		for _, target := range s.redirects[name].targets {
			if target.selector.Provider == "" && target.qualified == source {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// validateChains checks the redirect graph is a DAG no deeper than
// MaxChainDepth. It is a pure property of the declarations — independent of
// the provider catalog — so it runs whenever a snapshot is built, which covers
// admin writes, declarative config at startup, and background refreshes.
func validateChains(s *snapshot) error {
	depths := make(map[string]int, len(s.redirects))
	for _, source := range s.order {
		if _, err := chainDepth(s, source, depths, nil); err != nil {
			return err
		}
	}
	return nil
}

// chainDepth returns the depth of the redirect at source, memoizing in depths.
// path holds the sources on the current descent so a revisit is reported as a
// cycle spelled out in order.
func chainDepth(s *snapshot, source string, depths map[string]int, path []string) (int, error) {
	if depth, ok := depths[source]; ok {
		return depth, nil
	}
	for i, seen := range path {
		if seen == source {
			cycle := append(append([]string(nil), path[i:]...), source)
			return 0, newValidationError("virtual model chain forms a cycle: "+strings.Join(cycle, " -> "), nil)
		}
	}
	path = append(path, source)

	depth := 1
	for _, target := range s.redirects[source].targets {
		if _, ok := s.chained(source, target); !ok {
			continue
		}
		inner, err := chainDepth(s, target.qualified, depths, path)
		if err != nil {
			return 0, err
		}
		depth = max(depth, inner+1)
	}
	if depth > MaxChainDepth {
		return 0, newValidationError(fmt.Sprintf(
			"virtual model %q chains through more than %d virtual models", source, MaxChainDepth), nil)
	}
	depths[source] = depth
	return depth, nil
}
