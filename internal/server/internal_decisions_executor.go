package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// maxInternalDecisionBytes caps the answer an internal Decisions call reads.
// A Decision is a few hundred bytes per question.
const maxInternalDecisionBytes = 4 << 20

// InternalDecisionsExecutor answers gateway-internal Decisions requests,
// such as an llm_judge plugin asking a decision model, without an HTTP
// request or Echo context. It resolves the model, skips targets that cannot
// answer, fails over, caches, accounts usage, and audits as POST
// /v1/decisions does. No guardrail runs on the input: the caller is a
// guardrail, and its own question is not judged again.
type InternalDecisionsExecutor struct {
	service *translatedInferenceService
}

// NewInternalDecisionsExecutor creates the executor over provider, configured
// like the internal chat executor.
func NewInternalDecisionsExecutor(provider core.RoutableProvider, cfg InternalChatCompletionExecutorConfig) *InternalDecisionsExecutor {
	service := &translatedInferenceService{
		provider:               provider,
		modelResolver:          cfg.ModelResolver,
		modelAuthorizer:        cfg.ModelAuthorizer,
		workflowPolicyResolver: cfg.WorkflowPolicyResolver,
		failoverResolver:       cfg.FailoverResolver,
		failoverPolicy:         cfg.FailoverPolicy,
		logger:                 cfg.AuditLogger,
		usageLogger:            cfg.UsageLogger,
		pricingResolver:        cfg.PricingResolver,
		responseCache:          cfg.ResponseCache,
	}
	service.orchestrator = service.newInferenceOrchestrator()
	return &InternalDecisionsExecutor{service: service}
}

// Decide answers one OpenAI Decisions request body with the Decision body.
func (e *InternalDecisionsExecutor) Decide(ctx context.Context, body []byte) (answer []byte, err error) {
	s := e.service
	router, ok := s.provider.(core.RoutableDecisions)
	if !ok || len(router.DecisionsProviderTypes()) == 0 {
		return nil, core.NewNotFoundError("decisions are available only when an openai, jev, or openrouter provider is configured")
	}
	var req core.DecisionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, core.NewInvalidRequestError("invalid decision request: "+err.Error(), err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil).WithParam("model")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if core.GetRequestOrigin(ctx) != core.RequestOriginPlugin {
		ctx = core.WithRequestOrigin(ctx, core.RequestOriginGuardrail)
	}
	ctx = gateway.WithAttemptRecorder(ctx)

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	requested := core.NewRequestedModelSelector(req.Model, "")
	start := time.Now()
	entry := newInternalAuditEntry(s.logger, ctx, requestID, requested, decisionsRoute.path)
	var (
		workflow  *core.Workflow
		executed  core.ModelSelector
		meta      gateway.ExecutionMeta
		cacheType string
	)
	defer func() {
		finishInternalAuditEntry(s.logger, ctx, entry, internalExchange{
			path: decisionsRoute.path, start: start, workflow: workflow,
			request: json.RawMessage(body), response: rawJSONOrNil(answer),
			model: executed.Model, err: err, cacheType: cacheType, meta: meta,
		})
	}()

	resolution, err := e.resolve(ctx, requested)
	if err != nil {
		return nil, err
	}
	workflow, err = translatedWorkflow(ctx, requestID, core.DescribeEndpoint(http.MethodPost, decisionsRoute.path), resolution, s.workflowPolicyResolver)
	if err != nil {
		return nil, err
	}
	if reason := s.decisionsUnsupportedReason(router, resolution.ResolvedSelector, resolution.ProviderType); reason != "" {
		return nil, decisionsUnsupportedModelError(requestID, resolution, reason)
	}
	images := decisionBodyImages(body)
	if !s.readsImages(resolution.ResolvedSelector, resolution.ProviderType, images) {
		promoted, ok := s.imageReadingFailover(ctx, router, workflow, images)
		if !ok {
			return nil, unreadableImagesError(resolution, images)
		}
		workflow = promoted
	}
	eligible := func(selector core.ModelSelector, providerType string) bool {
		return s.decisionsUnsupportedReason(router, selector, providerType) == "" && s.readsImages(selector, providerType, images)
	}

	answer, executed, meta, cacheType, err = e.execute(ctx, router, workflow, body, eligible)
	if err != nil {
		return nil, err
	}
	if meta.UsedFailover {
		workflow = executedNativeWorkflow(workflow, executed, meta)
	}
	if cacheType == "" {
		s.orchestrator.LogUsage(ctx, workflow, executed.Model, meta.ProviderType, meta.ProviderName, func(pricing *core.ModelPricing) *usage.UsageEntry {
			return usage.ExtractFromJSONBody(answer, requestID, executed.Model, meta.ProviderType, decisionsRoute.path, pricing)
		})
	}
	return answer, nil
}

// resolve resolves the requested model as POST /v1/decisions does: a jev
// model the catalog does not list (a pinned version) goes to its jev
// provider, and the caller must be allowed to use the model.
func (e *InternalDecisionsExecutor) resolve(ctx context.Context, requested core.RequestedModelSelector) (*core.RequestModelResolution, error) {
	s := e.service
	if resolution, ok := s.unlistedJevResolution(ctx, requested); ok {
		if s.modelAuthorizer != nil {
			if err := s.modelAuthorizer.ValidateModelAccess(ctx, resolution.ResolvedSelector); err != nil {
				return nil, err
			}
		}
		return resolution, nil
	}
	return resolveRequestModelWithAuthorizer(ctx, s.provider, s.modelResolver, s.modelAuthorizer, requested)
}

// execute sends the body to the resolved target and its eligible failovers,
// through the exact response cache when the workflow allows it. The cache
// type is empty for a provider round trip.
func (e *InternalDecisionsExecutor) execute(
	ctx context.Context,
	router core.RoutableDecisions,
	workflow *core.Workflow,
	body []byte,
	eligible func(selector core.ModelSelector, providerType string) bool,
) ([]byte, core.ModelSelector, gateway.ExecutionMeta, string, error) {
	s := e.service
	if s.responseCache == nil || !workflow.CacheEnabled() {
		answer, executed, meta, err := e.dispatch(ctx, router, workflow, body, eligible)
		return answer, executed, meta, "", err
	}

	var (
		answer   []byte
		executed core.ModelSelector
		meta     gateway.ExecutionMeta
	)
	ctx = s.orchestrator.WithCacheRequestContext(ctx, workflow)
	result, err := s.responseCache.HandleInternalRequest(ctx, http.MethodPost, decisionsRoute.path, body, func(callCtx context.Context) (*responsecache.InternalResponse, error) {
		var execErr error
		answer, executed, meta, execErr = e.dispatch(callCtx, router, workflow, body, eligible)
		if execErr != nil {
			return nil, execErr
		}
		return &responsecache.InternalResponse{
			StatusCode:   http.StatusOK,
			ContentType:  "application/json",
			Body:         answer,
			FailoverUsed: meta.UsedFailover,
		}, nil
	})
	if err != nil {
		return nil, core.ModelSelector{}, gateway.ExecutionMeta{}, "", err
	}
	if result != nil && result.CacheType != "" {
		cachedMeta := gateway.ExecutionMeta{
			ProviderType: workflow.ProviderType,
			ProviderName: gateway.ProviderNameFromWorkflow(workflow),
		}
		return result.Body, workflow.Resolution.ResolvedSelector, cachedMeta, result.CacheType, nil
	}
	return answer, executed, meta, "", nil
}

// dispatch sends the body to the resolved target, and to the workflow's
// eligible failover targets while the failover policy allows, and reads the
// answer.
func (e *InternalDecisionsExecutor) dispatch(
	ctx context.Context,
	router core.RoutableDecisions,
	workflow *core.Workflow,
	body []byte,
	eligible func(selector core.ModelSelector, providerType string) bool,
) ([]byte, core.ModelSelector, gateway.ExecutionMeta, error) {
	headers := internalDecisionHeaders(ctx)
	resp, executed, meta, err := e.service.orchestrator.ExecutePassthroughWithFailover(ctx, workflow, eligible,
		func(ctx context.Context, selector core.ModelSelector, providerType, providerName string) (*core.PassthroughResponse, error) {
			return sendDecisions(ctx, router, selector, providerType, providerName, headers.Clone(), body)
		})
	if err != nil {
		return nil, core.ModelSelector{}, meta, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxInternalDecisionBytes))
	if err != nil {
		return nil, core.ModelSelector{}, meta, core.NewProviderError(meta.ProviderType, http.StatusBadGateway, "failed to read the decision", err)
	}
	return answer, executed, meta, nil
}

// internalDecisionHeaders are the headers an internal Decisions call sends:
// the JSON content type and the originating request's ID.
func internalDecisionHeaders(ctx context.Context) http.Header {
	headers := http.Header{"Content-Type": {"application/json"}}
	if requestID := strings.TrimSpace(core.GetRequestID(ctx)); requestID != "" {
		headers.Set(core.RequestIDHeader, requestID)
	}
	return headers
}

// rawJSONOrNil returns body as raw JSON for the audit entry, or nil when
// there is none.
func rawJSONOrNil(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	return json.RawMessage(body)
}
