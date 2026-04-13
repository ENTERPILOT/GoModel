package guardrails

import (
	"context"

	"gomodel/internal/core"
)

// ContextPipelineResolver resolves a request-scoped guardrails pipeline.
type ContextPipelineResolver interface {
	PipelineForContext(ctx context.Context) *Pipeline
}

// WorkflowRequestPatcher applies the guardrails pipeline selected by the current workflow.
type WorkflowRequestPatcher struct {
	resolver ContextPipelineResolver
}

// NewWorkflowRequestPatcher creates a translated-request patcher that resolves
// its pipeline from the request context on each call.
func NewWorkflowRequestPatcher(resolver ContextPipelineResolver) *WorkflowRequestPatcher {
	return &WorkflowRequestPatcher{resolver: resolver}
}

// PatchChatRequest applies the request-scoped guardrails pipeline to a translated chat request.
func (p *WorkflowRequestPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return processGuardedChat(ctx, p.pipeline(ctx), req)
}

// PatchResponsesRequest applies the request-scoped guardrails pipeline to a translated responses request.
func (p *WorkflowRequestPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return processGuardedResponses(ctx, p.pipeline(ctx), req)
}

func (p *WorkflowRequestPatcher) pipeline(ctx context.Context) *Pipeline {
	if p == nil || p.resolver == nil {
		return nil
	}
	return p.resolver.PipelineForContext(ctx)
}

// WorkflowBatchPreparer applies the guardrails pipeline selected by the current workflow.
type WorkflowBatchPreparer struct {
	provider core.RoutableProvider
	resolver ContextPipelineResolver
}

// NewWorkflowBatchPreparer creates a native-batch preparer that resolves its pipeline per request.
func NewWorkflowBatchPreparer(provider core.RoutableProvider, resolver ContextPipelineResolver) *WorkflowBatchPreparer {
	return &WorkflowBatchPreparer{
		provider: provider,
		resolver: resolver,
	}
}

// PrepareBatchRequest applies the request-scoped guardrails pipeline to native batch items.
func (p *WorkflowBatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return processGuardedBatchRequest(ctx, providerType, req, p.pipeline(ctx), p.batchFileTransport())
}

func (p *WorkflowBatchPreparer) batchFileTransport() core.BatchFileTransport {
	if p == nil || p.provider == nil {
		return nil
	}
	if files, ok := p.provider.(core.NativeFileRoutableProvider); ok {
		return files
	}
	return nil
}

func (p *WorkflowBatchPreparer) pipeline(ctx context.Context) *Pipeline {
	if p == nil || p.resolver == nil {
		return nil
	}
	return p.resolver.PipelineForContext(ctx)
}
