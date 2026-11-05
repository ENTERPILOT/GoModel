package virtualmodels

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// ReasoningEffortLevels are the reasoning effort values an override may force,
// in ascending order. "none" turns reasoning off where the provider allows it.
var ReasoningEffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// normalizeReasoningEffort trims and lower-cases an override, rejecting values
// outside ReasoningEffortLevels.
func normalizeReasoningEffort(effort string) (string, error) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || slices.Contains(ReasoningEffortLevels, effort) {
		return effort, nil
	}
	return "", newValidationError(fmt.Sprintf("unknown reasoning_effort %q (use one of: %s)", effort, strings.Join(ReasoningEffortLevels, ", ")), nil)
}

// setsOverrides reports whether the row forces anything onto requests: a
// reasoning effort or request parameters.
func (v VirtualModel) setsOverrides() bool {
	return v.ReasoningEffort != "" || len(v.Params) > 0 || len(v.DefaultParams) > 0
}

// overrides returns the row's per-request overrides.
func (v VirtualModel) overrides() requestparams.Overrides {
	return requestparams.Overrides{ReasoningEffort: v.ReasoningEffort, Defaults: v.DefaultParams, Params: v.Params}
}

// ResolveRequestOverrides returns what the operator forces on one provider
// attempt: the reasoning effort and the parameter sets, or a zero value to
// keep the client's request. Values layer from the outermost redirect to the
// concrete model, the deeper row winning: a policy on the concrete model the
// attempt goes to beats every redirect, and within a chain of redirects the
// one nearest the model beats the ones above it. Efforts replace; parameter
// sets merge per key. attempt is the concrete model of this attempt, which
// for a failover leg differs from the primary resolution. The primary attempt
// follows the route routing recorded on resolution; a failover leg, which
// carries only its concrete model, follows the first declared path to it.
func (s *Service) ResolveRequestOverrides(
	ctx context.Context,
	resolution *core.RequestModelResolution,
	attempt core.ModelSelector,
) requestparams.Overrides {
	if s == nil || resolution == nil {
		return requestparams.Overrides{}
	}
	snap := s.snapshot()
	if !snap.overrides {
		return requestparams.Overrides{}
	}
	userPath := core.UserPathFromContext(ctx)
	var resolved requestparams.Overrides
	if !resolution.Requested.ExplicitProvider {
		resolved = snap.redirectOverridesFor(resolution, attempt, userPath)
	}
	return resolved.Layer(snap.policyOverrides(attempt, userPath))
}

// redirectOverridesFor layers the overrides of every redirect on the path
// from the requested name to attempt, outermost first.
func (s *snapshot) redirectOverridesFor(resolution *core.RequestModelResolution, attempt core.ModelSelector, userPath string) requestparams.Overrides {
	var chain []*redirectEntry
	if len(resolution.VirtualModelPath) > 0 && attempt.QualifiedModel() == resolution.ResolvedSelector.QualifiedModel() {
		chain = s.routeChain(resolution.VirtualModelPath)
	} else if redirect, ok := s.findRedirect(resolution.Requested.Model, userPath, true); ok {
		if path, found := s.pathChain(redirect, attempt, nil); found {
			chain = path
		} else {
			chain = []*redirectEntry{redirect}
		}
	}
	var layered requestparams.Overrides
	for _, entry := range chain {
		layered = layered.Layer(entry.vm.overrides())
	}
	return layered
}

// policyOverrides returns what the model policies force on attempt: the
// model-wide policy layered under the exact one, so the exact policy wins
// per key and an exact policy that sets only parameters does not hide a
// model-wide effort. Only those two scopes carry per-model values.
func (s *snapshot) policyOverrides(attempt core.ModelSelector, userPath string) requestparams.Overrides {
	var resolved requestparams.Overrides
	model := strings.TrimSpace(attempt.Model)
	if model != "" {
		if policy, ok := s.modelWide[model]; ok && userPathAllowed(userPath, policy.UserPaths) {
			resolved = resolved.Layer(policy.overrides())
		}
	}
	if key := modelselectors.ExactMatchKey(strings.TrimSpace(attempt.Provider), model); key != "" {
		if policy, ok := s.exact[key]; ok && userPathAllowed(userPath, policy.UserPaths) {
			resolved = resolved.Layer(policy.overrides())
		}
	}
	return resolved
}

// routeChain returns the redirect entries of a recorded route, outermost
// first, skipping sources that are no longer redirects.
func (s *snapshot) routeChain(path []string) []*redirectEntry {
	chain := make([]*redirectEntry, 0, len(path))
	for _, source := range path {
		if entry, ok := s.redirects[source]; ok {
			chain = append(chain, entry)
		}
	}
	return chain
}

// pathChain walks entry's targets, descending enabled chained redirects, to
// the concrete target matching attempt, and returns the redirects on that
// path outermost first. found is false when no target leads to attempt.
// When several paths lead to the same concrete model, the first declared one
// decides: a failover leg carries only its concrete model, not a path to it.
func (s *snapshot) pathChain(entry *redirectEntry, attempt core.ModelSelector, above []*redirectEntry) (chain []*redirectEntry, found bool) {
	here := append(slices.Clone(above), entry)
	for _, target := range entry.targets {
		inner, chained := s.chained(entry.vm.Source, target)
		if !chained {
			if targetMatches(target, attempt) {
				return here, true
			}
			continue
		}
		if !inner.vm.Enabled {
			continue
		}
		if chain, found := s.pathChain(inner, attempt, here); found {
			return chain, true
		}
	}
	return nil, false
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
