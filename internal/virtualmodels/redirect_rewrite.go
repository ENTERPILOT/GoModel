package virtualmodels

import (
	"context"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// ChatExecutor applies virtual-model redirects to transport-free chat
// completions before delegating to the wrapped provider. It is the narrow
// replacement for the former redirect-aware Provider decorator, whose only
// production role was serving as the guardrail auxiliary-LLM executor
// (guardrails.ChatCompletionExecutor is a single-method interface).
type ChatExecutor struct {
	inner   ChatExecutorProvider
	service *Service
}

// ChatExecutorProvider is the slice of the router the executor needs: model
// support checks for redirect validation and chat dispatch.
type ChatExecutorProvider interface {
	modelSupportChecker
	ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)
}

// NewChatExecutor creates a redirect-aware chat executor over inner.
func NewChatExecutor(inner ChatExecutorProvider, service *Service) *ChatExecutor {
	return &ChatExecutor{inner: inner, service: service}
}

// ChatCompletion resolves the request's redirect (user-path aware) and
// delegates the rewritten request to the wrapped provider.
func (e *ChatExecutor) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	forward, err := rewriteChatRequest(ctx, e.service, e.inner, req)
	if err != nil {
		return nil, err
	}
	return e.inner.ChatCompletion(ctx, forward)
}

// --- shared redirect-rewrite helpers (used by ChatExecutor and BatchPreparer) ---

type modelSupportChecker interface {
	Supports(string) bool
}

type modelProviderTypeChecker interface {
	modelSupportChecker
	GetProviderType(string) string
}

// resolveRedirectRequestSelector resolves a request-time selector through the
// redirect table honoring the caller's user path, so a user_paths-scoped redirect
// is not applied for callers outside its scope (it falls through to the literal
// name).
func resolveRedirectRequestSelector(ctx context.Context, service *Service, requested core.RequestedModelSelector) (core.ModelSelector, error) {
	if service == nil {
		return requested.Normalize()
	}
	selector, changed, err := service.ResolveModelForUserPath(ctx, requested)
	if err != nil {
		return core.ModelSelector{}, err
	}
	if changed {
		return selector, nil
	}
	return requested.Normalize()
}

func resolveRedirectRoutableSelector(ctx context.Context, service *Service, checker modelSupportChecker, requested core.RequestedModelSelector, expectedProviderType string) (core.ModelSelector, error) {
	selector, err := resolveRedirectRequestSelector(ctx, service, requested)
	if err != nil {
		return core.ModelSelector{}, err
	}

	resolvedModel := strings.TrimSpace(selector.QualifiedModel())
	if resolvedModel == "" {
		return core.ModelSelector{}, core.NewInvalidRequestError("model is required", nil)
	}
	if checker == nil || !checker.Supports(resolvedModel) {
		return core.ModelSelector{}, core.NewModelNotFoundError(resolvedModel)
	}
	if err := validateResolvedProviderType(checker, selector, expectedProviderType); err != nil {
		return core.ModelSelector{}, err
	}
	return selector, nil
}

func validateResolvedProviderType(checker modelSupportChecker, selector core.ModelSelector, expectedProviderType string) error {
	expectedProviderType = strings.TrimSpace(expectedProviderType)
	if expectedProviderType == "" {
		return nil
	}

	actualProviderType := ""
	if typed, ok := checker.(modelProviderTypeChecker); ok {
		actualProviderType = strings.TrimSpace(typed.GetProviderType(selector.QualifiedModel()))
	}
	if actualProviderType == "" || actualProviderType == expectedProviderType {
		return nil
	}
	return core.NewInvalidRequestError(
		fmt.Sprintf(
			"native batch supports a single provider per batch; resolved model %q targets provider %q but batch provider is %q",
			selector.QualifiedModel(),
			actualProviderType,
			expectedProviderType,
		),
		nil,
	)
}

// rewriteChatRequest resolves a translated chat request's redirect (user-path
// aware) and rewrites it for routing (the resolved provider is preserved so
// downstream routing can pick the target). Batch rewriting (which clears the
// provider and enforces a single provider per batch) lives in batch_preparer.go.
func rewriteChatRequest(ctx context.Context, service *Service, checker modelSupportChecker, req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := resolveRedirectRoutableSelector(ctx, service, checker, core.NewRequestedModelSelector(req.Model, req.Provider), "")
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = selector.Provider
	return &forward, nil
}
