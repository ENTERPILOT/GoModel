package aliases

import (
	"context"
	"encoding/json"
	"strings"

	"gomodel/internal/core"
)

// BatchPreparer resolves aliases for native batch subrequests before provider
// submission. It is the explicit batch-only replacement for alias policy that
// previously lived inside the provider wrapper.
type BatchPreparer struct {
	provider core.RoutableProvider
	service  *Service
}

// NewBatchPreparer creates an explicit alias batch preparer.
func NewBatchPreparer(provider core.RoutableProvider, service *Service) *BatchPreparer {
	return &BatchPreparer{
		provider: provider,
		service:  service,
	}
}

// PrepareBatchRequest resolves aliases for batch subrequests without
// submitting the native batch to the wrapped provider.
func (p *BatchPreparer) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	return rewriteAliasBatchSource(ctx, providerType, req, p.service, p.provider, p.batchFileTransport())
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

type aliasModelSupportChecker interface {
	Supports(string) bool
}

func resolveAliasModel(service *Service, model, provider string) (core.ModelSelector, bool, error) {
	if service == nil {
		selector, err := core.ParseModelSelector(model, provider)
		return selector, false, err
	}
	resolution, ok, err := service.Resolve(model, provider)
	if err != nil {
		return core.ModelSelector{}, false, err
	}
	return resolution.Resolved, ok, nil
}

func resolveAliasRequestSelector(service *Service, model, provider string) (core.ModelSelector, error) {
	selector, changed, err := resolveAliasModel(service, model, provider)
	if err != nil {
		return core.ModelSelector{}, err
	}
	if changed {
		return selector, nil
	}
	return core.ParseModelSelector(model, provider)
}

func resolveAliasRoutableSelector(service *Service, checker aliasModelSupportChecker, model, provider string) (core.ModelSelector, error) {
	selector, err := resolveAliasRequestSelector(service, model, provider)
	if err != nil {
		return core.ModelSelector{}, err
	}

	resolvedModel := strings.TrimSpace(selector.QualifiedModel())
	if resolvedModel == "" {
		return core.ModelSelector{}, core.NewInvalidRequestError("model is required", nil)
	}
	if checker == nil || !checker.Supports(resolvedModel) {
		return core.ModelSelector{}, core.NewInvalidRequestError("unsupported model: "+resolvedModel, nil)
	}
	return selector, nil
}

func rewriteAliasChatRequest(service *Service, checker aliasModelSupportChecker, req *core.ChatRequest, mode requestRewriteMode) (*core.ChatRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := resolveAliasRoutableSelector(service, checker, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func rewriteAliasResponsesRequest(service *Service, checker aliasModelSupportChecker, req *core.ResponsesRequest, mode requestRewriteMode) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := resolveAliasRoutableSelector(service, checker, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func rewriteAliasEmbeddingRequest(service *Service, checker aliasModelSupportChecker, req *core.EmbeddingRequest, mode requestRewriteMode) (*core.EmbeddingRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := resolveAliasRoutableSelector(service, checker, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func rewriteAliasBatchSource(
	ctx context.Context,
	providerType string,
	req *core.BatchRequest,
	service *Service,
	checker aliasModelSupportChecker,
	fileTransport core.BatchFileTransport,
) (*core.BatchRewriteResult, error) {
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		fileTransport,
		[]string{"chat_completions", "responses", "embeddings"},
		func(_ context.Context, _ core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			switch typed := decoded.Request.(type) {
			case *core.ChatRequest:
				modified, err := rewriteAliasChatRequest(service, checker, typed, rewriteForUpstream)
				if err != nil {
					return nil, err
				}
				body, err := json.Marshal(modified)
				if err != nil {
					return nil, core.NewInvalidRequestError("failed to encode batch item", err)
				}
				return body, nil
			case *core.ResponsesRequest:
				modified, err := rewriteAliasResponsesRequest(service, checker, typed, rewriteForUpstream)
				if err != nil {
					return nil, err
				}
				body, err := json.Marshal(modified)
				if err != nil {
					return nil, core.NewInvalidRequestError("failed to encode batch item", err)
				}
				return body, nil
			case *core.EmbeddingRequest:
				modified, err := rewriteAliasEmbeddingRequest(service, checker, typed, rewriteForUpstream)
				if err != nil {
					return nil, err
				}
				body, err := json.Marshal(modified)
				if err != nil {
					return nil, core.NewInvalidRequestError("failed to encode batch item", err)
				}
				return body, nil
			default:
				return nil, core.NewInvalidRequestError("unsupported batch item url: "+decoded.Endpoint, nil)
			}
		},
	)
}
