// Package gateway contains transport-independent gateway use cases.
package gateway

import (
	"context"

	"gomodel/internal/core"
)

// ModelResolver resolves raw request selectors into concrete model selectors
// before provider execution.
type ModelResolver interface {
	ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error)
}

// FallbackResolver resolves alternate concrete model selectors for a translated
// request after the primary selector has already been resolved.
type FallbackResolver interface {
	ResolveFallbacks(resolution *core.RequestModelResolution, op core.Operation) []core.ModelSelector
}

// ModelAuthorizer validates request-scoped access to concrete models.
type ModelAuthorizer interface {
	ValidateModelAccess(ctx context.Context, selector core.ModelSelector) error
	AllowsModel(ctx context.Context, selector core.ModelSelector) bool
	FilterPublicModels(ctx context.Context, models []core.Model) []core.Model
}

// WorkflowPolicyResolver matches persisted workflow versions for requests.
type WorkflowPolicyResolver interface {
	Match(selector core.WorkflowSelector) (*core.ResolvedWorkflowPolicy, error)
}

// TranslatedRequestPatcher applies request-level transforms for translated
// routes after workflow resolution has resolved the concrete execution selector.
type TranslatedRequestPatcher interface {
	PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error)
	PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error)
}

// BatchRequestPreparer rewrites a native batch request before provider
// submission. This keeps batch-specific policy out of provider decorators.
type BatchRequestPreparer interface {
	PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error)
}
