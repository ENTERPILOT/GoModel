package modeloverrides

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gomodel/internal/core"
)

type selectorResolver interface {
	ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// BatchPreparer validates model access for native batch subrequests before provider submission.
type BatchPreparer struct {
	provider core.RoutableProvider
	service  *Service
}

// NewBatchPreparer creates an explicit model-override batch preparer.
func NewBatchPreparer(provider core.RoutableProvider, service *Service) *BatchPreparer {
	return &BatchPreparer{
		provider: provider,
		service:  service,
	}
}

// PrepareBatchRequest validates inline and file-backed batch items without rewriting them.
func (p *BatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		p.batchFileTransport(),
		[]core.Operation{core.OperationChatCompletions, core.OperationResponses, core.OperationEmbeddings},
		func(ctx context.Context, item core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			requested, err := requestedSelectorForDecodedRequest(decoded.Request)
			if err != nil {
				return nil, err
			}
			resolved, err := p.resolveSelector(requested)
			if err != nil {
				return nil, err
			}
			if p.provider != nil && !p.provider.Supports(resolved.QualifiedModel()) {
				return nil, core.NewInvalidRequestError("unsupported model: "+resolved.QualifiedModel(), nil)
			}
			if providerType != "" && p.provider != nil {
				actualProviderType := strings.TrimSpace(p.provider.GetProviderType(resolved.QualifiedModel()))
				if actualProviderType != "" && actualProviderType != providerType {
					return nil, core.NewInvalidRequestError(
						fmt.Sprintf(
							"native batch supports a single provider per batch; resolved model %q targets provider %q but batch provider is %q",
							resolved.QualifiedModel(),
							actualProviderType,
							providerType,
						),
						nil,
					)
				}
			}
			if p.service != nil {
				if err := p.service.ValidateModelAccess(ctx, resolved); err != nil {
					return nil, err
				}
			}
			return core.CloneRawJSON(item.Body), nil
		},
	)
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

func (p *BatchPreparer) resolveSelector(requested core.RequestedModelSelector) (core.ModelSelector, error) {
	if p == nil || p.provider == nil {
		return requested.Normalize()
	}
	if resolver, ok := p.provider.(selectorResolver); ok {
		selector, _, err := resolver.ResolveModel(requested)
		return selector, err
	}
	return requested.Normalize()
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
