package virtualmodels

import (
	"context"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
)

// BatchPreparer rewrites redirect (alias) sources for native batch subrequests
// and validates model access before provider submission. It is the combined
// replacement for the two old preparers (alias rewrite then access validation).
type BatchPreparer struct {
	provider core.RoutableProvider
	service  *Service
}

// NewBatchPreparer creates the combined redirect-rewrite + access-validation
// batch preparer.
func NewBatchPreparer(provider core.RoutableProvider, service *Service) *BatchPreparer {
	return &BatchPreparer{provider: provider, service: service}
}

// PrepareBatchRequest rewrites redirect sources for inline and file-backed batch
// items and validates model access for each resolved selector.
func (p *BatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		p.batchFileTransport(),
		[]core.Operation{core.OperationChatCompletions, core.OperationResponses, core.OperationEmbeddings},
		func(ctx context.Context, _ core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			requested, err := requestedSelectorForDecodedRequest(decoded.Request)
			if err != nil {
				return nil, err
			}
			// Resolve the redirect target and verify catalog support + single
			// provider per batch, mirroring the alias rewrite pass.
			resolved, err := resolveRedirectRoutableSelector(ctx, p.service, p.provider, requested, providerType)
			if err != nil {
				return nil, err
			}
			// Validate access against the resolved selector, mirroring the
			// access-override pass.
			if p.service != nil {
				if err := p.service.ValidateModelAccess(ctx, resolved); err != nil {
					return nil, err
				}
			}
			return rewriteDecodedBatchItem(decoded.Request, resolved)
		},
	)
}

func rewriteDecodedBatchItem(request any, resolved core.ModelSelector) (json.RawMessage, error) {
	switch typed := request.(type) {
	case *core.ChatRequest:
		forward := *typed
		forward.Model = resolved.Model
		forward.Provider = ""
		return marshalBatchItem(&forward)
	case *core.ResponsesRequest:
		forward := *typed
		forward.Model = resolved.Model
		forward.Provider = ""
		return marshalBatchItem(&forward)
	case *core.EmbeddingRequest:
		forward := *typed
		forward.Model = resolved.Model
		forward.Provider = ""
		return marshalBatchItem(&forward)
	default:
		return nil, core.NewInvalidRequestError("unsupported batch item request", nil)
	}
}

func requestedSelectorForDecodedRequest(request any) (core.RequestedModelSelector, error) {
	switch typed := request.(type) {
	case *core.ChatRequest:
		return core.NewRequestedModelSelector(typed.Model, typed.Provider), nil
	case *core.ResponsesRequest:
		return core.NewRequestedModelSelector(typed.Model, typed.Provider), nil
	case *core.EmbeddingRequest:
		return core.NewRequestedModelSelector(typed.Model, typed.Provider), nil
	default:
		return core.RequestedModelSelector{}, core.NewInvalidRequestError("unsupported batch item request", nil)
	}
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
