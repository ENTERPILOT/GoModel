package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/responsecache"
)

// dispatchSystemOneWithCache serves a guarded System One body from the
// response cache when the workflow allows it, and forwards it otherwise.
// Only the exact layer applies: the key covers the route, the resolved model,
// the guardrail chain, and the forwarded body, and the semantic layer never
// serves these paths, since a similar state is not the same decision.
func (s *translatedInferenceService) dispatchSystemOneWithCache(c *echo.Context, route systemOneRoute, workflow *core.Workflow, body []byte) error {
	dispatch := func() error { return s.dispatchSystemOne(c, route, workflow, body) }
	if s.responseCache == nil || !workflow.CacheEnabled() {
		return dispatch()
	}
	c.SetRequest(c.Request().WithContext(s.inference().WithCacheRequestContext(c.Request().Context(), workflow)))
	err := s.responseCache.HandleRequest(c, body, dispatch)
	if replayErr, ok := errors.AsType[*responsecache.ReplayError](err); ok {
		recordCachedStreamError(c, replayErr.Err)
		return nil
	}
	return err
}

// dispatchSystemOne forwards the body to the resolved route, and to the
// workflow's failover targets while the failover policy allows, then relays
// the answer unchanged with audit and usage accounting.
func (s *translatedInferenceService) dispatchSystemOne(c *echo.Context, route systemOneRoute, workflow *core.Workflow, body []byte) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}
	// Record each provider attempt so the audit entry shows a failed primary
	// and the failover that answered, as it does for chat.
	c.SetRequest(c.Request().WithContext(gateway.WithAttemptRecorder(c.Request().Context())))
	s.observeLiveProviderAttempts(c, workflow)

	failovers := len(s.inference().FailoverSelectors(workflow))
	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRouteFromWorkflow(workflow).withFailovers(failovers))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx := adm.dispatchContext(c.Request().Context())

	headers := buildPassthroughHeaders(ctx, c.Request().Header)
	// The client's Idempotency-Key reaches the primary through the request
	// context, which failover attempts clear; forwarded as an explicit header
	// it would also mark every failover target's different body.
	headers.Del(core.IdempotencyKeyHeader)
	eligible := func(selector core.ModelSelector, providerType string) bool {
		return s.systemOneUnsupportedReason(route, selector, providerType) == ""
	}
	resp, executed, meta, err := s.inference().ExecutePassthroughWithFailover(ctx, workflow, eligible,
		func(ctx context.Context, selector core.ModelSelector, providerType, providerName string) (*core.PassthroughResponse, error) {
			return s.sendSystemOne(ctx, passthroughProvider, route, selector, providerType, providerName, headers, body)
		})
	enrichAuditEntryWithProviderAttempts(c)
	if err != nil {
		return handleError(c, err)
	}

	if meta.UsedFailover {
		markRequestFailoverUsed(c)
		auditlog.EnrichEntryWithFailover(c, meta.FailoverModel)
		workflow = executedSystemOneWorkflow(workflow, executed, meta)
		storeWorkflow(c, workflow)
	}
	auditlog.EnrichEntryWithWorkflow(c, workflow)
	auditlog.EnrichEntryWithResolvedRoute(c, executed.QualifiedModel(), meta.ProviderType, meta.ProviderName)
	info := &core.PassthroughRouteInfo{
		Provider:           meta.ProviderType,
		ProviderName:       meta.ProviderName,
		NormalizedEndpoint: route.endpoint,
		SemanticOperation:  route.operation,
		AuditPath:          route.path,
		Model:              executed.Model,
	}
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, meta.ProviderType, meta.ProviderName, route.endpoint, info, resp)
}

// maxSystemOneErrorBodyBytes caps how much of an upstream error body is read
// to build the gateway error, so a misbehaving upstream cannot make the
// gateway buffer an unbounded body.
const maxSystemOneErrorBodyBytes = 64 << 10

// sendSystemOne sends the body to one target under its own model name. Only
// targets that serve the route reach it: the handler checks the primary and
// the failover sweep skips ineligible targets. An upstream error status comes
// back as an error the failover policy can judge.
func (s *translatedInferenceService) sendSystemOne(
	ctx context.Context,
	passthroughProvider core.RoutablePassthrough,
	route systemOneRoute,
	selector core.ModelSelector,
	providerType, providerName string,
	headers http.Header,
	body []byte,
) (*core.PassthroughResponse, error) {
	forwarded, err := rewriteMessagesModel(body, selector.Model)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     route.endpoint,
		Operation:    route.operation,
		Model:        selector.Model,
		Body:         io.NopCloser(bytes.NewReader(forwarded)),
		Headers:      headers.Clone(),
		ProviderName: providerName,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty passthrough response", nil)
	}
	if resp.StatusCode < http.StatusBadRequest {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	errorBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSystemOneErrorBodyBytes))
	if err != nil {
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "failed to read provider error response", err)
	}
	return nil, core.ParseProviderError(providerType, resp.StatusCode, errorBody, nil)
}

// executedSystemOneWorkflow returns a copy of workflow routed to the failover
// target that answered, so usage is priced and audited under the model that
// did the work rather than the primary that failed.
func executedSystemOneWorkflow(workflow *core.Workflow, executed core.ModelSelector, meta gateway.ExecutionMeta) *core.Workflow {
	if workflow == nil || workflow.Resolution == nil {
		return workflow
	}
	resolution := *workflow.Resolution
	resolution.ResolvedSelector = executed
	resolution.ProviderType = strings.TrimSpace(meta.ProviderType)
	resolution.ProviderName = strings.TrimSpace(meta.ProviderName)
	cloned := *workflow
	cloned.ProviderType = resolution.ProviderType
	cloned.Resolution = &resolution
	return &cloned
}
