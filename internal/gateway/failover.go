package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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
	eligible func(selector core.ModelSelector, providerType string) bool,
	call func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, ExecutionMeta, error) {
	var zero T

	// A canceled or expired context means the client is gone or the deadline
	// passed. A failover call on a done context can never succeed; attempting one
	// only wastes attempts and charges spurious failures to healthy failover
	// providers' circuit breakers. Short-circuit to the primary error instead.
	if ctx.Err() != nil {
		return zero, ExecutionMeta{}, primaryErr
	}

	failovers := o.FailoverSelectors(workflow)
	if len(failovers) == 0 || !o.failoverPolicy.ShouldRetry(primaryErr) {
		return zero, ExecutionMeta{}, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	attempts := 0
	for _, selector := range failovers {
		// Stop sweeping if the client disconnected mid-failover or the policy
		// cap on failover attempts is reached.
		if ctx.Err() != nil || o.failoverPolicy.attemptsExhausted(attempts) {
			break
		}
		if o.modelAuthorizer != nil && !o.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := o.ProviderTypeForSelector(selector, ProviderTypeFromWorkflow(workflow))
		providerName := ResolvedProviderName(o.provider, selector, ProviderNameFromWorkflow(workflow))
		// A target that cannot serve the request is skipped before it counts
		// against the attempt cap, so it never crowds out a later valid one.
		if eligible != nil && !eligible(selector, providerType) {
			slog.Info("skipping failover target that cannot serve the request",
				"request_id", requestID,
				"to", qualified,
				"provider_type", providerType,
			)
			continue
		}
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
		attempts++
		resp, resolvedProviderType, err := call(selector, providerType, providerName)
		recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, firstNonEmptyString(resolvedProviderType, providerType), providerName, qualified, started, err))
		if err == nil {
			slog.Info("failover model attempt succeeded",
				"request_id", requestID,
				"from", primaryModel,
				"to", qualified,
				"provider_type", resolvedProviderType,
			)
			return resp, ExecutionMeta{
				ProviderType:  resolvedProviderType,
				ProviderName:  providerName,
				FailoverModel: qualified,
				UsedFailover:  true,
			}, nil
		}
		lastErr = err
	}

	return zero, ExecutionMeta{}, lastErr
}

func executeWithFailoverResponse[T any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	model, provider string,
	primary func() (T, string, string, error),
	eligible func(selector core.ModelSelector, providerType string) bool,
	failoverFn func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, ExecutionMeta, error) {
	resp, resolvedProviderType, resolvedProviderName, err := primary()
	if err == nil {
		return resp, ExecutionMeta{ProviderType: resolvedProviderType, ProviderName: resolvedProviderName}, nil
	}
	return tryFailoverResponse(ctx, o, workflow, model, provider, err, eligible, failoverFn)
}

func executeTranslatedWithFailover[Req any, Resp any](
	ctx context.Context,
	o *InferenceOrchestrator,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req, string) (Resp, string, error),
) (Resp, ExecutionMeta, error) {
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
			resp, responseProvider, err := call(ctx, req, ProviderNameFromWorkflow(workflow))
			attemptProviderType := ResponseProviderType(ProviderTypeFromWorkflow(workflow), responseProvider)
			recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, attemptProviderType, ProviderNameFromWorkflow(workflow), currentSelectorForWorkflow(workflow, model, provider), started, err))
			if err != nil {
				return zero, "", "", err
			}
			return resp, ResponseProviderType(ProviderTypeFromWorkflow(workflow), responseProvider), ProviderNameFromWorkflow(workflow), nil
		},
		nil,
		func(selector core.ModelSelector, providerType, providerName string) (Resp, string, error) {
			// A failover target gets a different request body, so it must not
			// reuse the client's idempotency key.
			resp, responseProvider, err := call(core.WithIdempotencyKey(ctx, ""), cloneForSelector(req, selector), providerName)
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
) (io.ReadCloser, ExecutionMeta, error) {
	// See tryFailoverResponse: never sweep failover targets once the context is
	// done, or the doomed attempts pollute healthy providers' circuit breakers.
	if ctx.Err() != nil {
		return nil, ExecutionMeta{}, primaryErr
	}

	failovers := o.FailoverSelectors(workflow)
	if len(failovers) == 0 || !o.failoverPolicy.ShouldRetry(primaryErr) {
		return nil, ExecutionMeta{}, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	attempts := 0
	for _, selector := range failovers {
		// Stop sweeping if the client disconnected mid-failover or the policy
		// cap on failover attempts is reached.
		if ctx.Err() != nil || o.failoverPolicy.attemptsExhausted(attempts) {
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
		attempts++
		stream, resolvedProviderType, usageModel, err := call(selector, providerType, providerName)
		recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindFailover, firstNonEmptyString(resolvedProviderType, providerType), providerName, qualified, started, err))
		if err == nil {
			slog.Info("failover stream attempt succeeded",
				"request_id", requestID,
				"from", primaryModel,
				"to", qualified,
				"provider_type", resolvedProviderType,
			)
			return stream, ExecutionMeta{
				ProviderType:  resolvedProviderType,
				ProviderName:  providerName,
				Model:         usageModel,
				FailoverModel: qualified,
				UsedFailover:  true,
			}, nil
		}
		lastErr = err
	}

	return nil, ExecutionMeta{}, lastErr
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// PassthroughCall sends one native request to selector's provider. Provider
// error statuses must come back as errors so the failover policy can judge
// them.
type PassthroughCall func(ctx context.Context, selector core.ModelSelector, providerType, providerName string) (*core.PassthroughResponse, error)

// ExecutePassthroughWithFailover runs a native, untranslated request against
// the workflow's resolved route and then, while the failover policy allows,
// against its failover targets. It is the native-endpoint counterpart of the
// translated failover path: attempts are recorded the same way, but every
// target receives the client's own dialect, so eligible must reject a
// failover target that cannot serve it; rejected targets are skipped without
// counting against the attempt cap. The selector that answered is returned
// with the response.
func (o *InferenceOrchestrator) ExecutePassthroughWithFailover(ctx context.Context, workflow *core.Workflow, eligible func(selector core.ModelSelector, providerType string) bool, call PassthroughCall) (*core.PassthroughResponse, core.ModelSelector, ExecutionMeta, error) {
	primary := core.ModelSelector{}
	if workflow != nil && workflow.Resolution != nil {
		primary = workflow.Resolution.ResolvedSelector
	}
	type answer struct {
		resp     *core.PassthroughResponse
		selector core.ModelSelector
	}
	result, meta, err := executeWithFailoverResponse(ctx, o, workflow, primary.Model, primary.Provider,
		func() (answer, string, string, error) {
			started := time.Now()
			providerType, providerName := ProviderTypeFromWorkflow(workflow), ProviderNameFromWorkflow(workflow)
			qualified := primary.QualifiedModel()
			// A rate-saturated primary route must not reach the provider; its
			// stored 429 becomes the primary failure that starts the sweep.
			if saturated := core.PrimaryRouteSaturated(ctx); saturated != nil {
				recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, providerType, providerName, qualified, started, saturated))
				return answer{}, "", "", saturated
			}
			resp, err := call(ctx, primary, providerType, providerName)
			recordProviderAttempt(ctx, providerAttemptFromResult(AttemptKindPrimary, providerType, providerName, qualified, started, err))
			if err != nil {
				return answer{}, "", "", err
			}
			return answer{resp: resp, selector: primary}, providerType, providerName, nil
		},
		eligible,
		func(selector core.ModelSelector, providerType, providerName string) (answer, string, error) {
			// A failover target gets a different body, so it must not reuse
			// the client's idempotency key.
			resp, err := call(core.WithIdempotencyKey(ctx, ""), selector, providerType, providerName)
			if err != nil {
				return answer{}, "", err
			}
			return answer{resp: resp, selector: selector}, providerType, nil
		},
	)
	return result.resp, result.selector, meta, err
}
