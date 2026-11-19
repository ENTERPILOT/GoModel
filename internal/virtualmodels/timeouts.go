package virtualmodels

import (
	"context"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// setsTimeouts reports whether the row overrides any upstream timeout.
func (v VirtualModel) setsTimeouts() bool {
	return v.Timeout != nil || v.ResponseHeaderTimeout != nil
}

// ResolveTimeouts returns the per-attempt upstream timeouts for a request.
// Each field resolves on its own, as slowdown does: the redirects routing
// passed through win, outermost first, then the resolved model's exact
// policy, then its model-wide one. A zero field inherits the gateway-wide
// HTTP timeout. route is the virtual-model path recorded by resolution;
// without one the requested redirect is looked up by name.
func (s *Service) ResolveTimeouts(
	ctx context.Context,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
	route []string,
) core.UpstreamTimeouts {
	if s == nil {
		return core.UpstreamTimeouts{}
	}
	snap := s.snapshot()
	userPath := core.UserPathFromContext(ctx)

	var rows []VirtualModel
	if !requested.ExplicitProvider {
		chain := snap.routeChain(route)
		if len(chain) == 0 {
			if redirect, ok := snap.findRedirect(requested.Model, userPath, true); ok {
				chain = append(chain, redirect)
			}
		}
		for _, entry := range chain {
			rows = append(rows, entry.vm)
		}
	}
	if policy, ok := snap.modelPolicy(resolved.Provider, resolved.Model, userPath, setsTimeout); ok {
		rows = append(rows, policy)
	}
	if policy, ok := snap.modelPolicy(resolved.Provider, resolved.Model, userPath, setsResponseHeaderTimeout); ok {
		rows = append(rows, policy)
	}

	var timeouts core.UpstreamTimeouts
	for _, row := range rows {
		if timeouts.Timeout == 0 && row.Timeout != nil {
			timeouts.Timeout = time.Duration(*row.Timeout) * time.Second
		}
		if timeouts.ResponseHeaderTimeout == 0 && row.ResponseHeaderTimeout != nil {
			timeouts.ResponseHeaderTimeout = time.Duration(*row.ResponseHeaderTimeout) * time.Second
		}
	}
	return timeouts
}

func setsTimeout(policy VirtualModel) bool {
	return policy.Timeout != nil
}

func setsResponseHeaderTimeout(policy VirtualModel) bool {
	return policy.ResponseHeaderTimeout != nil
}
