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
// a failover leg differs from the primary resolution.
func (s *Service) ResolveReasoningEffort(
	ctx context.Context,
	requested core.RequestedModelSelector,
	attempt core.ModelSelector,
) string {
	if s == nil {
		return ""
	}

	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)
	if policy, ok := snap.matchingPolicy(attempt.Provider, attempt.Model); ok &&
		policy.ReasoningEffort != "" && userPathAllowed(userPath, policy.UserPaths) {
		return policy.ReasoningEffort
	}
	if requested.ExplicitProvider {
		return ""
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

// pathReasoningEffort walks entry's targets, descending enabled chained
// redirects, to the concrete target matching attempt, and returns the effort
// of the deepest redirect on that path that sets one. found is false when no
// target leads to attempt.
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
