package guardrails

import (
	"context"
	"io"

	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
)

// GuardedProvider is a test harness that exercises the live guardrail
// request-rewrite functions (processGuardedChat / processGuardedResponses /
// processGuardedBatchRequest) end to end, the same way the production
// server-side patchers (WorkflowRequestPatcher / WorkflowBatchPreparer) do.
//
// Production wires the pipeline through those Workflow* patchers; this wrapper
// exists only so the shared rewrite logic can be tested against a real inner
// provider in one place.
type GuardedProvider struct {
	inner    core.RoutableProvider
	pipeline *Pipeline
	options  GuardedProviderOptions
}

// GuardedProviderOptions mirrors the batch-processing toggle exercised by the tests.
type GuardedProviderOptions struct {
	EnableForBatchProcessing bool
}

func NewGuardedProvider(inner core.RoutableProvider, pipeline *Pipeline) *GuardedProvider {
	return NewGuardedProviderWithOptions(inner, pipeline, GuardedProviderOptions{})
}

func NewGuardedProviderWithOptions(inner core.RoutableProvider, pipeline *Pipeline, options GuardedProviderOptions) *GuardedProvider {
	return &GuardedProvider{inner: inner, pipeline: pipeline, options: options}
}

func (g *GuardedProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	modified, err := processGuardedChat(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.ChatCompletion(ctx, modified)
}

func (g *GuardedProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	modified, err := processGuardedChat(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.StreamChatCompletion(ctx, modified)
}

func (g *GuardedProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	modified, err := processGuardedResponses(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.Responses(ctx, modified)
}

func (g *GuardedProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	modified, err := processGuardedResponses(ctx, g.pipeline, req)
	if err != nil {
		return nil, err
	}
	return g.inner.StreamResponses(ctx, modified)
}

func (g *GuardedProvider) nativeBatchRouter() (core.NativeBatchRoutableProvider, error) {
	bp, ok := g.inner.(core.NativeBatchRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil)
	}
	return bp, nil
}

func (g *GuardedProvider) nativeFileRouter() (core.NativeFileRoutableProvider, error) {
	fp, ok := g.inner.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return fp, nil
}

func (g *GuardedProvider) batchFileTransport() core.BatchFileTransport {
	files, err := g.nativeFileRouter()
	if err != nil {
		return nil
	}
	return files
}

// CreateBatch applies guardrails to inline batch items (when enabled) before
// delegating native batch creation, mirroring the production submit/cleanup
// orchestration so that path stays covered.
func (g *GuardedProvider) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	bp, err := g.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	if !g.options.EnableForBatchProcessing {
		return bp.CreateBatch(ctx, providerType, req)
	}

	result, err := processGuardedBatchRequest(ctx, providerType, req, g.pipeline, g.batchFileTransport())
	if err != nil {
		return nil, err
	}
	batchrewrite.RecordPreparation(ctx, req, result.Request)
	resp, err := bp.CreateBatch(ctx, providerType, result.Request)
	if err != nil {
		batchrewrite.CleanupFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
		return nil, err
	}
	batchrewrite.CleanupSupersededFileFromRouter(ctx, g.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
	return resp, nil
}

func (g *GuardedProvider) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	if !g.options.EnableForBatchProcessing {
		return &core.BatchRewriteResult{Request: req}, nil
	}
	return processGuardedBatchRequest(ctx, providerType, req, g.pipeline, g.batchFileTransport())
}
