package virtualmodels

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResolveSlowdown returns the request-scoped extra-time factor for a resolved
// model. A matching alias setting takes precedence over its concrete target;
// otherwise the normal exact/provider/model/global policy precedence applies.
// The context makes the lookup ready for user-path-specific settings without
// changing the request execution interface later.
func (s *Service) ResolveSlowdown(
	ctx context.Context,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
) float64 {
	if s == nil {
		return 0
	}

	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)
	if !requested.ExplicitProvider {
		if redirect, ok := snap.findRedirect(requested.Model, userPath, true); ok && redirect.vm.Slowdown != nil {
			return *redirect.vm.Slowdown
		}
	}

	if policy, ok := snap.matchingPolicy(resolved.Provider, resolved.Model); ok &&
		policy.Slowdown != nil && s.identityAllowed(userPath, policy.UserPaths, policy.Groups) {
		return *policy.Slowdown
	}
	return 0
}
