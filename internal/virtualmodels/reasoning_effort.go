package virtualmodels

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ReasoningEffortLevels are the reasoning effort values an override may force,
// from least to most reasoning. Providers map each onto the closest level the
// upstream model accepts.
var ReasoningEffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// normalizeReasoningEffort trims and lower-cases an override, rejecting values
// no provider translates. Empty means no override.
func normalizeReasoningEffort(effort string) (string, error) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || slices.Contains(ReasoningEffortLevels, effort) {
		return effort, nil
	}
	return "", newValidationError(fmt.Sprintf("unknown reasoning_effort %q (use one of: %s)", effort, strings.Join(ReasoningEffortLevels, ", ")), nil)
}

// ResolveReasoningEffort returns the reasoning effort to force on one provider
// attempt, or "" to keep the client's value. The deepest configured value
// wins: a policy on the concrete model the attempt goes to beats every
// redirect, and within a chain of redirects the one nearest the model beats
// the ones above it. attempt is the concrete model of this attempt, which for
// a failover leg differs from the primary resolution. The primary attempt
// follows the route routing recorded on resolution; a failover leg, which
// carries only its concrete model, follows the first declared path to it.
func (s *Service) ResolveReasoningEffort(
	ctx context.Context,
	resolution *core.RequestModelResolution,
	attempt core.ModelSelector,
) string {
	if s == nil || resolution == nil {
		return ""
	}
	requested := resolution.Requested

	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)
	if policy, ok := snap.matchingPolicy(attempt.Provider, attempt.Model); ok &&
		policy.ReasoningEffort != "" && userPathAllowed(userPath, policy.UserPaths) {
		return policy.ReasoningEffort
	}
	if requested.ExplicitProvider {
		return ""
	}
	if len(resolution.VirtualModelPath) > 0 && attempt.QualifiedModel() == resolution.ResolvedSelector.QualifiedModel() {
		return snap.routeReasoningEffort(resolution.VirtualModelPath)
	}
	redirect, ok := snap.findRedirect(requested.Model, userPath, true)
	if !ok {
		return ""
	}
	if effort, found := snap.pathReasoningEffort(redirect, attempt); found {
		return effort
	}
	return redirect.vm.ReasoningEffort
}

// routeReasoningEffort returns the effort of the deepest virtual model on a
// recorded route that sets one.
func (s *snapshot) routeReasoningEffort(path []string) string {
	for _, source := range slices.Backward(path) {
		if entry, ok := s.redirects[source]; ok && entry.vm.ReasoningEffort != "" {
			return entry.vm.ReasoningEffort
		}
	}
	return ""
}

// pathReasoningEffort walks entry's targets, descending enabled chained
// redirects, to the concrete target matching attempt, and returns the effort
// of the deepest redirect on that path that sets one. found is false when no
// target leads to attempt. When several paths lead to the same concrete model,
// the first declared one decides: a failover leg carries only its concrete
// model, not a path to it.
func (s *snapshot) pathReasoningEffort(entry *redirectEntry, attempt core.ModelSelector) (effort string, found bool) {
	for _, target := range entry.targets {
		inner, chained := s.chained(entry.vm.Source, target)
		if !chained {
			if targetMatches(target, attempt) {
				return entry.vm.ReasoningEffort, true
			}
			continue
		}
		if !inner.vm.Enabled {
			continue
		}
		if effort, found := s.pathReasoningEffort(inner, attempt); found {
			if effort == "" {
				effort = entry.vm.ReasoningEffort
			}
			return effort, true
		}
	}
	return "", false
}

// targetMatches reports whether a concrete redirect target is the model an
// attempt goes to. A target declared without a provider matches the model on
// whichever provider the router picked for it.
func targetMatches(target resolvedTarget, attempt core.ModelSelector) bool {
	if target.qualified == attempt.QualifiedModel() {
		return true
	}
	return target.selector.Provider == "" && target.selector.Model == attempt.Model
}
