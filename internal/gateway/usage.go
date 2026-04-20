package gateway

import (
	"context"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// LogUsage writes one non-streaming usage entry when usage is enabled.
func (o *InferenceOrchestrator) LogUsage(
	ctx context.Context,
	workflow *core.Workflow,
	model, providerType, providerName string,
	extractFn func(*core.ModelPricing) *usage.UsageEntry,
) {
	o.logUsage(ctx, workflow, model, providerType, providerName, extractFn)
}

func (o *InferenceOrchestrator) logUsage(
	ctx context.Context,
	workflow *core.Workflow,
	model, providerType, providerName string,
	extractFn func(*core.ModelPricing) *usage.UsageEntry,
) {
	if o.usageLogger == nil || !o.usageLogger.Config().Enabled || (workflow != nil && !workflow.UsageEnabled()) {
		return
	}
	var pricing *core.ModelPricing
	if o.pricingResolver != nil {
		pricing = o.pricingResolver.ResolvePricing(model, providerType)
	}
	if entry := extractFn(pricing); entry != nil {
		entry.ProviderName = strings.TrimSpace(providerName)
		entry.UserPath = core.UserPathFromContext(ctx)
		o.usageLogger.Write(entry)
	}
}

// ShouldEnforceReturningUsageData reports whether streams should request usage chunks.
func (o *InferenceOrchestrator) ShouldEnforceReturningUsageData() bool {
	if o.usageLogger == nil {
		return false
	}
	cfg := o.usageLogger.Config()
	return cfg.Enabled && cfg.EnforceReturningUsageData
}
