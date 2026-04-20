package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"gomodel/internal/core"
)

// FallbackSelectors returns fallback selectors for a translated workflow.
func (o *InferenceOrchestrator) FallbackSelectors(workflow *core.Workflow) []core.ModelSelector {
	if o.fallbackResolver == nil || workflow == nil || workflow.Resolution == nil || !workflow.FallbackEnabled() {
		return nil
	}
	return o.fallbackResolver.ResolveFallbacks(workflow.Resolution, workflow.Endpoint.Operation)
}

// ProviderTypeForSelector returns the provider type for a selector.
func (o *InferenceOrchestrator) ProviderTypeForSelector(selector core.ModelSelector, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if o.provider == nil {
		if provider := strings.TrimSpace(selector.Provider); provider != "" {
			return provider
		}
		return fallback
	}
	if providerType := strings.TrimSpace(o.provider.GetProviderType(selector.QualifiedModel())); providerType != "" {
		return providerType
	}
	if provider := strings.TrimSpace(selector.Provider); provider != "" {
		return provider
	}
	return fallback
}

func tryFallbackResponse[T any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	var zero T

	fallbacks := o.FallbackSelectors(workflow)
	if len(fallbacks) == 0 || !ShouldAttemptFallback(primaryErr) {
		return zero, "", "", "", false, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		if o.modelAuthorizer != nil && !o.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := o.ProviderTypeForSelector(selector, ProviderTypeFromWorkflow(workflow))
		providerName := ResolvedProviderName(o.provider, selector, ProviderNameFromWorkflow(workflow))
		slog.Warn("primary model attempt failed, trying fallback",
			"request_id", requestID,
			"from", primaryModel,
			"to", qualified,
			"provider_type", providerType,
			"error", lastErr,
		)

		resp, resolvedProviderType, err := call(selector, providerType, providerName)
		if err == nil {
			slog.Info("fallback model attempt succeeded",
				"request_id", requestID,
				"from", primaryModel,
				"to", qualified,
				"provider_type", resolvedProviderType,
			)
			return resp, resolvedProviderType, providerName, qualified, true, nil
		}
		lastErr = err
	}

	return zero, "", "", "", false, lastErr
}

func executeWithFallbackResponse[T any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primary func() (T, string, string, error),
	fallback func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	resp, resolvedProviderType, resolvedProviderName, err := primary()
	if err == nil {
		return resp, resolvedProviderType, resolvedProviderName, "", false, nil
	}
	return tryFallbackResponse(ctx, o, workflow, model, provider, err, fallback)
}

func executeTranslatedWithFallback[Req any, Resp any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (Resp, string, error),
) (Resp, string, string, string, bool, error) {
	return executeWithFallbackResponse(ctx, o, workflow, model, provider,
		func() (Resp, string, string, error) {
			resp, responseProvider, err := call(ctx, req)
			if err != nil {
				var zero Resp
				return zero, "", "", err
			}
			return resp, ResponseProviderType(ProviderTypeFromWorkflow(workflow), responseProvider), ProviderNameFromWorkflow(workflow), nil
		},
		func(selector core.ModelSelector, providerType, providerName string) (Resp, string, error) {
			resp, responseProvider, err := call(ctx, cloneForSelector(req, selector))
			if err != nil {
				var zero Resp
				return zero, "", err
			}
			return resp, ResponseProviderType(providerType, responseProvider), nil
		},
	)
}

func tryFallbackStream(
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error),
) (io.ReadCloser, string, string, string, string, error) {
	fallbacks := o.FallbackSelectors(workflow)
	if len(fallbacks) == 0 || !ShouldAttemptFallback(primaryErr) {
		return nil, "", "", "", "", primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		if o.modelAuthorizer != nil && !o.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := o.ProviderTypeForSelector(selector, ProviderTypeFromWorkflow(workflow))
		providerName := ResolvedProviderName(o.provider, selector, ProviderNameFromWorkflow(workflow))
		slog.Warn("primary model attempt failed, trying fallback stream",
			"request_id", requestID,
			"from", primaryModel,
			"to", qualified,
			"provider_type", providerType,
			"error", lastErr,
		)

		stream, resolvedProviderType, usageModel, err := call(selector, providerType, providerName)
		if err == nil {
			slog.Info("fallback stream attempt succeeded",
				"request_id", requestID,
				"from", primaryModel,
				"to", qualified,
				"provider_type", resolvedProviderType,
			)
			return stream, resolvedProviderType, providerName, usageModel, qualified, nil
		}
		lastErr = err
	}

	return nil, "", "", "", "", lastErr
}

// ShouldAttemptFallback reports whether err should trigger translated fallback.
func ShouldAttemptFallback(err error) bool {
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil {
		return false
	}

	status := gatewayErr.HTTPStatusCode()
	if status >= http.StatusInternalServerError || status == http.StatusTooManyRequests {
		return true
	}

	code := ""
	if gatewayErr.Code != nil {
		code = strings.ToLower(strings.TrimSpace(*gatewayErr.Code))
	}
	if code != "" && strings.Contains(code, "model") &&
		(strings.Contains(code, "not_found") || strings.Contains(code, "unsupported") || strings.Contains(code, "unavailable")) {
		return true
	}

	message := strings.ToLower(strings.TrimSpace(gatewayErr.Message))
	if !strings.Contains(message, "model") {
		return false
	}

	for _, fragment := range []string{
		"not found",
		"does not exist",
		"unsupported",
		"unavailable",
		"not available",
		"deprecated",
		"retired",
		"disabled",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}

	return false
}
