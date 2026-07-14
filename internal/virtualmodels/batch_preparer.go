package virtualmodels

import (
	"context"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
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
	return rewriteBatchSource(ctx, providerType, req, p.service, p.provider, p.batchFileTransport(), p.validateAccess)
}

// validateAccess enforces the access policy for one resolved batch selector.
func (p *BatchPreparer) validateAccess(ctx context.Context, resolved core.ModelSelector) error {
	if p.service == nil {
		return nil
	}
	return p.service.ValidateModelAccess(ctx, resolved)
}

// batchFileTransport returns the provider's native file transport when it can
// rewrite file-backed batch requests directly.
func (p *BatchPreparer) batchFileTransport() core.BatchFileTransport {
	if p == nil || p.provider == nil {
		return nil
	}
	if files, ok := p.provider.(core.NativeFileRoutableProvider); ok {
		return files
	}
	return nil
}

// rewriteBatchSource resolves redirects for inline and file-backed batch items
// and rewrites each for upstream submission. validate, when non-nil, is called
// with the resolved selector before rewriting — the server-side preparer enforces
// access there; the provider wrapper passes nil.
func rewriteBatchSource(
	ctx context.Context,
	providerType string,
	req *core.BatchRequest,
	service *Service,
	checker modelSupportChecker,
	fileTransport core.BatchFileTransport,
	validate func(context.Context, core.ModelSelector) error,
) (*core.BatchRewriteResult, error) {
	return core.RewriteBatchSource(
		ctx,
		providerType,
		req,
		fileTransport,
		[]core.Operation{core.OperationChatCompletions, core.OperationResponses, core.OperationEmbeddings},
		func(ctx context.Context, _ core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
			return rewriteBatchItem(ctx, service, checker, providerType, decoded, validate)
		},
	)
}

// rewriteBatchItem resolves one decoded batch item's redirect (verifying catalog
// support and single-provider-per-batch), optionally validates access, then
// re-encodes the item for upstream. It is the single per-item rewrite shared by
// the provider wrapper and the server-side preparer.
func rewriteBatchItem(
	ctx context.Context,
	service *Service,
	checker modelSupportChecker,
	providerType string,
	decoded *core.DecodedBatchItemRequest,
	validate func(context.Context, core.ModelSelector) error,
) (json.RawMessage, error) {
	requested, err := decoded.RequestedModelSelector()
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	// resolveRedirectRoutableSelector is user-path aware (scoped redirects), so a
	// caller outside a scoped alias's user_paths gets the literal name here too.
	resolved, err := resolveRedirectRoutableSelector(ctx, service, checker, requested, providerType)
	if err != nil {
		return nil, err
	}
	if validate != nil {
		if err := validate(ctx, resolved); err != nil {
			return nil, err
		}
	}
	return rewriteDecodedBatchItem(decoded.Request, resolved)
}

// rewriteDecodedBatchItem writes the resolved model into a supported decoded
// batch request and clears the provider before upstream submission.
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

// marshalBatchItem encodes a rewritten batch item as JSON for the upstream
// provider payload.
func marshalBatchItem(v any) (json.RawMessage, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode batch item", err)
	}
	return body, nil
}
