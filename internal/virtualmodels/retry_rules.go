package virtualmodels

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// setsRetryRules reports whether the row overrides any retry behavior.
func (v VirtualModel) setsRetryRules() bool { return len(v.RetryRules) > 0 }

// ResolveRetryRules returns the per-status retry rules for a request. Rules
// follow the same inheritance order as timeouts: the redirects routing
// actually applied win, outermost first, then the resolved model's exact
// policy, then its model-wide one. An empty result keeps the gateway retry
// policy. route is the virtual-model path recorded by resolution.
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
	sets := func(vm VirtualModel) bool { return len(vm.RetryRules) > 0 }
	for _, entry := range chain {
		if sets(entry.vm) {
			return retryRulesToCore(entry.vm.RetryRules)
		}
	}
	if policy, ok := snap.modelPolicy(resolved.Provider, resolved.Model, userPath, sets); ok {
		return retryRulesToCore(policy.RetryRules)
	}
	return nil
}

func retryRulesToCore(rules []RetryRule) core.UpstreamRetryRules {
	if len(rules) == 0 {
		return nil
	}
	out := make(core.UpstreamRetryRules, len(rules))
	for i, rule := range rules {
		out[i] = core.UpstreamRetryRule{
			Status:     rule.Status,
			MaxRetries: rule.MaxRetries,
			Delay:      rule.Delay,
		}
	}
	return out
}
