package guardrails

import (
	"context"
	"encoding/json"

	"gomodel/internal/core"
)

// RequestPatcher applies guardrails to translated requests without owning
// provider execution.
type RequestPatcher struct {
	pipeline *Pipeline
}

// NewRequestPatcher creates an explicit translated-request patcher.
func NewRequestPatcher(pipeline *Pipeline) *RequestPatcher {
	return &RequestPatcher{pipeline: pipeline}
}

// PatchChatRequest applies guardrails to a translated chat request.
func (p *RequestPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return processGuardedChat(ctx, p.pipeline, req)
}

// PatchResponsesRequest applies guardrails to a translated responses request.
func (p *RequestPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	return processGuardedResponses(ctx, p.pipeline, req)
}

// BatchPreparer applies guardrails to native batch subrequests before provider
// submission.
type BatchPreparer struct {
	provider core.RoutableProvider
	pipeline *Pipeline
}

// NewBatchPreparer creates an explicit native-batch preparer.
func NewBatchPreparer(provider core.RoutableProvider, pipeline *Pipeline) *BatchPreparer {
	return &BatchPreparer{
		provider: provider,
		pipeline: pipeline,
	}
}

// PrepareBatchRequest applies guardrails to batch subrequests without
// submitting the batch to the wrapped provider.
func (p *BatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return processGuardedBatchRequest(ctx, providerType, req, p.pipeline, p.batchFileTransport())
}

func (p *BatchPreparer) batchFileTransport() core.BatchFileTransport {
	if p == nil || p.provider == nil {
		return nil
	}
	if files, ok := p.provider.(core.NativeFileRoutableProvider); ok {
		return files
	}
	return nil
}

func processGuardedBatchRequest(
	ctx context.Context,
	providerType string,
	req *core.BatchRequest,
	pipeline *Pipeline,
	fileTransport core.BatchFileTransport,
) (*core.BatchRewriteResult, error) {
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		fileTransport,
		[]string{"chat_completions", "responses"},
		func(ctx context.Context, item core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			itemBody := core.CloneRawJSON(item.Body)
			return core.DispatchDecodedBatchItem(decoded, core.DecodedBatchItemHandlers[json.RawMessage]{
				Chat: func(original *core.ChatRequest) (json.RawMessage, error) {
					modified, err := processGuardedChat(ctx, pipeline, original)
					if err != nil {
						return nil, err
					}
					body, err := rewriteGuardedChatBatchBody(itemBody, original, modified)
					if err != nil {
						return nil, core.NewInvalidRequestError("failed to encode guarded chat batch item", err)
					}
					return body, nil
				},
				Responses: func(original *core.ResponsesRequest) (json.RawMessage, error) {
					modified, err := processGuardedResponses(ctx, pipeline, original)
					if err != nil {
						return nil, err
					}
					body, err := rewriteGuardedResponsesBatchBody(itemBody, modified)
					if err != nil {
						return nil, core.NewInvalidRequestError("failed to encode guarded responses batch item", err)
					}
					return body, nil
				},
			})
		},
	)
}

func processGuardedChat(ctx context.Context, pipeline *Pipeline, req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	msgs, err := chatToMessages(req)
	if err != nil {
		return nil, err
	}
	if pipeline == nil || pipeline.Len() == 0 {
		return req, nil
	}
	modified, err := pipeline.Process(ctx, msgs)
	if err != nil {
		return nil, err
	}
	return applyMessagesToChatPreservingEnvelope(req, modified)
}

func processGuardedResponses(ctx context.Context, pipeline *Pipeline, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	if pipeline == nil || pipeline.Len() == 0 || req == nil {
		return req, nil
	}
	msgs := responsesToMessages(req)
	modified, err := pipeline.Process(ctx, msgs)
	if err != nil {
		return nil, err
	}
	return applyMessagesToResponses(req, modified), nil
}
