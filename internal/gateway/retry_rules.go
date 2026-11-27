package gateway

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// withAttemptRetryRules attaches the rules of the concrete target one
// provider attempt goes to. Attaching even an empty list matters: without it
// the client would fall back to the primary resolution's rules.
func (o *InferenceOrchestrator) withAttemptRetryRules(ctx context.Context, attempt core.ModelSelector) context.Context {
	if httpclient.HasAttemptRetryRules(ctx) {
		return ctx
	}
	workflow := core.GetWorkflow(ctx)
	resolver, ok := o.modelResolver.(ModelAttemptRetryRulesResolver)
	if !ok || workflow == nil || workflow.Resolution == nil {
		return ctx
	}
	resolved := resolver.ResolveAttemptRetryRules(ctx, workflow.Resolution, attempt)
	rules := make([]httpclient.AttemptRetryRule, len(resolved))
	for i, rule := range resolved {
		rules[i] = httpclient.AttemptRetryRule{
			Status:     rule.Status,
			MaxRetries: rule.MaxRetries,
			Delay:      rule.Delay,
		}
	}
	return httpclient.WithAttemptRetryRules(ctx, rules)
}
