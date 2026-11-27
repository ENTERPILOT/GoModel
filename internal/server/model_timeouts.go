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
	route []string,
) context.Context {
	timeoutsResolver, ok := resolver.(gateway.ModelTimeoutsResolver)
	if !ok {
		return ctx
	}
	timeouts := timeoutsResolver.ResolveTimeouts(ctx, requested, resolved, route)
	return httpclient.WithAttemptTimeouts(ctx, httpclient.AttemptTimeouts{
		Timeout:               timeouts.Timeout,
		ResponseHeaderTimeout: timeouts.ResponseHeaderTimeout,
	})
}

// withModelRetryRules attaches the per-status retry rules configured for the
// model to ctx. Endpoints that run without a workflow (audio, images) use it;
// workflow-backed requests resolve them per concrete attempt instead.
func withModelRetryRules(
	ctx context.Context,
	resolver RequestModelResolver,
	requested core.RequestedModelSelector,
	resolved core.ModelSelector,
	route []string,
) context.Context {
	retryResolver, ok := resolver.(gateway.ModelRetryRulesResolver)
	if !ok {
		return ctx
	}
	resolvedRules := retryResolver.ResolveRetryRules(ctx, requested, resolved, route)
	rules := make([]httpclient.AttemptRetryRule, len(resolvedRules))
	for i, rule := range resolvedRules {
		rules[i] = httpclient.AttemptRetryRule{
			Status:     rule.Status,
			MaxRetries: rule.MaxRetries,
			Delay:      rule.Delay,
		}
	}
	return httpclient.WithAttemptRetryRules(ctx, rules)
}
