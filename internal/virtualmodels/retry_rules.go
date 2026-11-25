package virtualmodels

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
)

// setsRetryRules reports whether the row overrides any retry behavior.
func (v VirtualModel) setsRetryRules() bool { return len(v.RetryRules) > 0 }

// ResolveRetryRules returns the per-status retry rules for a request. Rules
// merge per status in the same inheritance order as timeouts: the redirects
// routing actually applied win, outermost first, then the resolved model's
// exact policy, then its model-wide one. An empty result keeps the gateway
// retry policy. route is the virtual-model path recorded by resolution.
func (s *Service) ResolveRetryRules(
	ctx context.Context,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
	route []string,
) core.UpstreamRetryRules {
	if s == nil {
		return nil
	}
	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)

	var chain []*redirectEntry
	if !requested.ExplicitProvider {
		chain = snap.routeChain(route)
	}
	return snap.retryRulesFor(chain, resolved, userPath)
}

// ResolveAttemptRetryRules returns the rules for one concrete provider
// attempt. The primary attempt follows the route recorded on resolution; a
// failover leg follows the first declared redirect path to its own target, so
// one target's policy never leaks into another.
func (s *Service) ResolveAttemptRetryRules(
	ctx context.Context,
	resolution *core.RequestModelResolution,
	attempt core.ModelSelector,
) core.UpstreamRetryRules {
	if s == nil || resolution == nil {
		return nil
	}
	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)

	var chain []*redirectEntry
	if !resolution.Requested.ExplicitProvider {
		if len(resolution.VirtualModelPath) > 0 && attempt.QualifiedModel() == resolution.ResolvedSelector.QualifiedModel() {
			chain = snap.routeChain(resolution.VirtualModelPath)
		} else if redirect, ok := snap.findRedirect(resolution.Requested.Model, userPath, true); ok {
			if path, found := snap.pathChain(redirect, attempt, nil); found {
				chain = path
			} else {
				chain = []*redirectEntry{redirect}
			}
		}
	}
	return snap.retryRulesFor(chain, attempt, userPath)
}

// retryRulesFor merges rules per status. The first row in precedence order
// that names a status owns it; rows lower in order still fill the statuses
// they alone configure.
func (s *snapshot) retryRulesFor(chain []*redirectEntry, resolved core.ModelSelector, userPath string) core.UpstreamRetryRules {
	seen := make(map[int]struct{})
	var out core.UpstreamRetryRules
	add := func(rules []RetryRule) {
		for _, rule := range rules {
			if _, ok := seen[rule.Status]; ok {
				continue
			}
			seen[rule.Status] = struct{}{}
			maxRetries := 0
			if rule.MaxRetries != nil {
				maxRetries = *rule.MaxRetries
			}
			out = append(out, core.UpstreamRetryRule{
				Status:     rule.Status,
				MaxRetries: maxRetries,
				Delay:      rule.Delay,
			})
		}
	}
	for _, entry := range chain {
		add(entry.vm.RetryRules)
	}
	if key := modelselectors.ExactMatchKey(resolved.Provider, resolved.Model); key != "" {
		if policy, ok := s.exact[key]; ok && userPathAllowed(userPath, policy.UserPaths) {
			add(policy.RetryRules)
		}
	}
	if resolved.Model != "" {
		if policy, ok := s.modelWide[resolved.Model]; ok && userPathAllowed(userPath, policy.UserPaths) {
			add(policy.RetryRules)
		}
	}
	return out
}
