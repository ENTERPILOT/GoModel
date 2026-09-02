package providers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// requestedProviderName returns the trimmed provider instance name a
// passthrough request asks for, or "" when it names none.
func requestedProviderName(req *core.PassthroughRequest) string {
	if req == nil {
		return ""
	}
	return strings.TrimSpace(req.ProviderName)
}

// Passthrough routes an opaque provider-native request by provider type.
// If req.ProviderName is set, routing prefers the named provider instance over
// the first registered provider of the given type.
func (r *Router) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	var pp core.PassthroughProvider
	if providerName := requestedProviderName(req); providerName != "" {
		slog.DebugContext(ctx, "passthrough routing by name", "providerName", req.ProviderName, "providerType", providerType)
		if p := r.providerByNameRegistry(providerName); p != nil {
			if named, ok := p.(core.PassthroughProvider); ok {
				pp = named
				slog.DebugContext(ctx, "passthrough routed by name", "providerName", req.ProviderName)
			} else {
				slog.DebugContext(ctx, "passthrough provider found by name but does not implement PassthroughProvider", "providerName", req.ProviderName)
			}
		} else {
			slog.DebugContext(ctx, "passthrough provider not found by name, falling back to type", "providerName", req.ProviderName, "providerType", providerType)
		}
	}
	if pp == nil {
		var err error
		pp, err = r.resolvePassthroughProvider(providerType)
		if err != nil {
			return nil, err
		}
	}
	return pp.Passthrough(ctx, req)
}
