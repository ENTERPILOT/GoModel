package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gomodel/internal/core"
)

// FailoverSelectors returns failover selectors for a translated workflow.
func (o *InferenceOrchestrator) FailoverSelectors(workflow *core.Workflow) []core.ModelSelector {
	if o.failoverResolver == nil || workflow == nil || workflow.Resolution == nil || !workflow.FailoverEnabled() {
		return nil
	}
	return o.failoverResolver.ResolveFailovers(workflow.Resolution, workflow.Endpoint.Operation)
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

func tryFailoverResponse[T any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	var zero T

	// A canceled or expired context means the client is gone or the deadline
	// passed. A failover call on a done context can never succeed; attempting one
	// only wastes attempts and charges spurious failures to healthy failover
	// providers' circuit breakers. Short-circuit to the primary error instead.
	if ctx.Err() != nil {
		return zero, "", "", "", false, primaryErr
	}

	failovers := o.FailoverSelectors(workflow)
	if len(failovers) == 0 || !ShouldAttemptFailover(primaryErr) {
		return zero, "", "", "", false, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range failovers {
		// Stop sweeping if the client disconnected mid-failover.
		if ctx.Err() != nil {
			break
		}
		if o.modelAuthorizer != nil && !o.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := o.ProviderTypeForSelector(selector, ProviderTypeFromWorkflow(workflow))
		providerName := ResolvedProviderName(o.provider, selector, ProviderNameFromWorkflow(workflow))
		if o.routeGate != nil && !o.routeGate.RouteAvailable(providerName, qualified) {
			slog.Info("skipping rate-limited failover target",
				"request_id", requestID,
				"to", qualified,
				"provider", providerName,
			)
			continue
		}
		slog.Warn("primary model attempt failed, trying failover",
			"request_id", requestID,
			"from", primaryModel,
			"to", qualified,
			"provider_type", providerType,
			"error", lastErr,
		)

		started := time.Now()
		resp, resolvedProviderType, err := call(selector, providerType, providerName)
		recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, firstNonEmptyString(resolvedProviderType, providerType), providerName, qualified, started, err))
		if err == nil {
			slog.Info("failover model attempt succeeded",
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

func executeWithFailoverResponse[T any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primary func() (T, string, string, error),
	failoverFn func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	resp, resolvedProviderType, resolvedProviderName, err := primary()
	if err == nil {
		return resp, resolvedProviderType, resolvedProviderName, "", false, nil
	}
	return tryFailoverResponse(ctx, o, workflow, model, provider, err, failoverFn)
}

func executeTranslatedWithFailover[Req any, Resp any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (Resp, string, error),
) (Resp, string, string, string, bool, error) {
	return executeWithFailoverResponse(ctx, o, workflow, model, provider,
		func() (Resp, string, string, error) {
			started := time.Now()
			var zero Resp
			// A rate-saturated primary route must not reach the provider (the
			// upstream would happily serve it and defeat the limit); its
			// stored 429 becomes the primary failure that starts the sweep.
			if saturated := core.PrimaryRouteSaturated(ctx); saturated != nil {
				recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, ProviderTypeFromWorkflow(workflow), ProviderNameFromWorkflow(workflow), currentSelectorForWorkflow(workflow, model, provider), started, saturated))
				return zero, "", "", saturated
			}
			resp, responseProvider, err := call(ctx, req)
			attemptProviderType := ResponseProviderType(ProviderTypeFromWorkflow(workflow), responseProvider)
			recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, attemptProviderType, ProviderNameFromWorkflow(workflow), currentSelectorForWorkflow(workflow, model, provider), started, err))
			if err != nil {
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

func tryFailoverStream(
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error),
) (io.ReadCloser, string, string, string, string, error) {
	// See tryFailoverResponse: never sweep failover targets once the context is
	// done, or the doomed attempts pollute healthy providers' circuit breakers.
	if ctx.Err() != nil {
		return nil, "", "", "", "", primaryErr
	}

	failovers := o.FailoverSelectors(workflow)
	if len(failovers) == 0 || !ShouldAttemptFailover(primaryErr) {
		return nil, "", "", "", "", primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range failovers {
		// Stop sweeping if the client disconnected mid-failover.
		if ctx.Err() != nil {
			break
		}
		if o.modelAuthorizer != nil && !o.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := o.ProviderTypeForSelector(selector, ProviderTypeFromWorkflow(workflow))
		providerName := ResolvedProviderName(o.provider, selector, ProviderNameFromWorkflow(workflow))
		if o.routeGate != nil && !o.routeGate.RouteAvailable(providerName, qualified) {
			slog.Info("skipping rate-limited failover target",
				"request_id", requestID,
				"to", qualified,
				"provider", providerName,
			)
			continue
		}
		slog.Warn("primary model attempt failed, trying failover stream",
			"request_id", requestID,
			"from", primaryModel,
			"to", qualified,
			"provider_type", providerType,
			"error", lastErr,
		)

		started := time.Now()
		stream, resolvedProviderType, usageModel, err := call(selector, providerType, providerName)
		recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, firstNonEmptyString(resolvedProviderType, providerType), providerName, qualified, started, err))
		if err == nil {
			slog.Info("failover stream attempt succeeded",
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

// ShouldAttemptFailover reports whether err should trigger translated failover.
func ShouldAttemptFailover(err error) bool {
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
	if strings.Contains(message, "model") {
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
	}

	if status == http.StatusNotFound {
		for _, fragment := range []string{
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
	}

	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
