package guardrails

import (
	"context"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func processGuardedBatchRequest(
	ctx context.Context,
	providerType string,
	req *core.BatchRequest,
	pipeline *Pipeline,
	fileTransport core.BatchFileTransport,
) (*core.BatchRewriteResult, error) {
	if pipeline == nil || pipeline.Len() == 0 || req == nil {
		return &core.BatchRewriteResult{Request: req}, nil
	}
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		fileTransport,
		[]core.Operation{core.OperationChatCompletions, core.OperationResponses},
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
	if pipeline == nil || pipeline.Len() == 0 || req == nil {
		return req, nil
	}
	msgs, err := chatToMessages(req)
	if err != nil {
		return nil, err
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
	msgs, err := responsesToMessages(req)
	if err != nil {
		return nil, err
	}
	modified, err := pipeline.Process(ctx, msgs)
	if err != nil {
		return nil, err
	}
	return applyMessagesToResponses(req, modified)
}
