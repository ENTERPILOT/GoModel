package guardrails

import (
	"context"
	"errors"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/pluginapi"
)

// WorkflowBatchPreparer runs the prompt chain selected by the current
// workflow over native batch items.
type WorkflowBatchPreparer struct {
	provider core.RoutableProvider
	resolver ContextChainsResolver
}

// NewWorkflowBatchPreparer creates a native-batch preparer that resolves its
// chains per request.
func NewWorkflowBatchPreparer(provider core.RoutableProvider, resolver ContextChainsResolver) *WorkflowBatchPreparer {
	return &WorkflowBatchPreparer{provider: provider, resolver: resolver}
}

// PrepareBatchRequest applies the request-scoped prompt chain to native batch items.
func (p *WorkflowBatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return processGuardedBatchRequest(ctx, providerType, req, promptChain(p.resolver, ctx), p.batchFileTransport())
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

func processGuardedBatchRequest(
	ctx context.Context,
	providerType string,
	req *core.BatchRequest,
	chain *plugins.Chain,
	fileTransport core.BatchFileTransport,
) (*core.BatchRewriteResult, error) {
	if chain.Empty() || req == nil {
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
					modified, err := processGuardedChat(ctx, chain, original)
					if err != nil {
						return nil, batchItemError(err)
					}
					body, err := rewriteGuardedChatBatchBody(itemBody, original, modified)
					if err != nil {
						return nil, core.NewInvalidRequestError("failed to encode guarded chat batch item", err)
					}
					return body, nil
				},
				Responses: func(original *core.ResponsesRequest) (json.RawMessage, error) {
					modified, err := processGuardedResponses(ctx, chain, original)
					if err != nil {
						return nil, batchItemError(err)
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

// batchItemError maps a respond short-circuit to a rejection: a batch item
// cannot be answered by a plugin.
func batchItemError(err error) error {
	if short, ok := errors.AsType[*plugins.ShortCircuit](err); ok {
		return plugins.BlockError(short.Decision, plugins.DefaultBlockStatus(pluginapi.KindPrompt))
	}
	return err
}
