package guardrails

import (
	"context"

	"gomodel/internal/core"
)

// ContextPipelineResolver resolves a request-scoped guardrails pipeline.
type ContextPipelineResolver interface {
	PipelineForContext(ctx context.Context) *Pipeline
}

// PlannedRequestPatcher applies the guardrails pipeline selected by the current execution plan.
type PlannedRequestPatcher struct {
	resolver ContextPipelineResolver
}

// NewPlannedRequestPatcher creates a translated-request patcher that resolves
// its pipeline from the request context on each call.
func NewPlannedRequestPatcher(resolver ContextPipelineResolver) *PlannedRequestPatcher {
	return &PlannedRequestPatcher{resolver: resolver}
}

// PatchChatRequest applies the request-scoped guardrails pipeline to a translated chat request.
func (p *PlannedRequestPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return processGuardedChat(ctx, p.pipeline(ctx), req)
}

// PatchResponsesRequest applies the request-scoped guardrails pipeline to a translated responses request.
func (p *PlannedRequestPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return processGuardedResponses(ctx, p.pipeline(ctx), req)
}

func (p *PlannedRequestPatcher) pipeline(ctx context.Context) *Pipeline {
	if p == nil || p.resolver == nil {
		return nil
	}
	return p.resolver.PipelineForContext(ctx)
}

// PlannedBatchPreparer applies the guardrails pipeline selected by the current execution plan.
type PlannedBatchPreparer struct {
	provider core.RoutableProvider
	resolver ContextPipelineResolver
}

// NewPlannedBatchPreparer creates a native-batch preparer that resolves its pipeline per request.
func NewPlannedBatchPreparer(provider core.RoutableProvider, resolver ContextPipelineResolver) *PlannedBatchPreparer {
	return &PlannedBatchPreparer{
		provider: provider,
		resolver: resolver,
	}
}

// PrepareBatchRequest applies the request-scoped guardrails pipeline to native batch items.
func (p *PlannedBatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return processGuardedBatchRequest(ctx, providerType, req, p.pipeline(ctx), p.batchFileTransport())
}

func (p *PlannedBatchPreparer) batchFileTransport() core.BatchFileTransport {
	if p == nil || p.provider == nil {
		return nil
	}
	if files, ok := p.provider.(core.NativeFileRoutableProvider); ok {
		return files
	}
	return nil
}

func (p *PlannedBatchPreparer) pipeline(ctx context.Context) *Pipeline {
	if p == nil || p.resolver == nil {
		return nil
	}
	return p.resolver.PipelineForContext(ctx)
}
