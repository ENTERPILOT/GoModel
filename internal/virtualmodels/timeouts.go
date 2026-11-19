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
// HTTP timeout. route is the virtual-model path recorded by resolution.
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

	// Only redirects routing actually applied count: a redirect that sets a
	// timeout turns on route recording, so an empty route means none did.
	var chain []*redirectEntry
	if !requested.ExplicitProvider {
		chain = snap.routeChain(route)
	}
	// Each field is looked up on its own, so a row that sets only one field
	// never decides the other.
	resolve := func(field func(VirtualModel) *int) time.Duration {
		for _, entry := range chain {
			if value := field(entry.vm); value != nil {
				return time.Duration(*value) * time.Second
			}
		}
		sets := func(policy VirtualModel) bool { return field(policy) != nil }
		if policy, ok := snap.modelPolicy(resolved.Provider, resolved.Model, userPath, sets); ok {
			return time.Duration(*field(policy)) * time.Second
		}
		return 0
	}
	return core.UpstreamTimeouts{
		Timeout:               resolve(func(vm VirtualModel) *int { return vm.Timeout }),
		ResponseHeaderTimeout: resolve(func(vm VirtualModel) *int { return vm.ResponseHeaderTimeout }),
	}
}
