package server

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// withModelTimeouts attaches the per-attempt upstream timeouts configured for
// the model to ctx. Endpoints that run without a workflow (audio, images)
// use it; workflow-backed requests carry the timeouts on their resolution.
func withModelTimeouts(
	ctx context.Context,
	resolver RequestModelResolver,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
) context.Context {
	timeoutsResolver, ok := resolver.(gateway.ModelTimeoutsResolver)
	if !ok {
		return ctx
	}
	timeouts := timeoutsResolver.ResolveTimeouts(ctx, requested, resolved, nil)
	return httpclient.WithAttemptTimeouts(ctx, httpclient.AttemptTimeouts{
		Timeout:               timeouts.Timeout,
		ResponseHeaderTimeout: timeouts.ResponseHeaderTimeout,
	})
}
