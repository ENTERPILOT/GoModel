package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/usage"
)

// InternalChatCompletionExecutorConfig configures the transport-free translated
// chat execution path used by gateway-owned workflows such as guardrails.
type InternalChatCompletionExecutorConfig struct {
	ModelResolver          RequestModelResolver
	ModelAuthorizer        RequestModelAuthorizer
	WorkflowPolicyResolver RequestWorkflowPolicyResolver
	FailoverResolver       RequestFailoverResolver
	FailoverPolicy         *gateway.FailoverPolicy
	AuditLogger            auditlog.LoggerInterface
	UsageLogger            usage.LoggerInterface
	PricingResolver        usage.PricingResolver
	ResponseCache          *responsecache.ResponseCacheMiddleware
}

// InternalChatCompletionExecutor executes internal translated chat requests
// without synthesizing an HTTP request or Echo context.
type InternalChatCompletionExecutor struct {
	provider               core.RoutableProvider
	modelResolver          RequestModelResolver
	workflowPolicyResolver RequestWorkflowPolicyResolver
	logger                 auditlog.LoggerInterface
	orchestrator           *gateway.InferenceOrchestrator
	modelAuthorizer        RequestModelAuthorizer
	responseCache          *responsecache.ResponseCacheMiddleware
}

// NewInternalChatCompletionExecutor creates a transport-free translated chat
// executor that reuses workflow resolution, failover, usage, and audit logic.
func NewInternalChatCompletionExecutor(provider core.RoutableProvider, cfg InternalChatCompletionExecutorConfig) *InternalChatCompletionExecutor {
	return &InternalChatCompletionExecutor{
		provider:               provider,
		modelResolver:          cfg.ModelResolver,
		modelAuthorizer:        cfg.ModelAuthorizer,
		workflowPolicyResolver: cfg.WorkflowPolicyResolver,
		logger:                 cfg.AuditLogger,
		responseCache:          cfg.ResponseCache,
		orchestrator: gateway.NewInferenceOrchestrator(gateway.InferenceConfig{
			Provider:                 provider,
			ModelResolver:            cfg.ModelResolver,
			ModelAuthorizer:          cfg.ModelAuthorizer,
			WorkflowPolicyResolver:   cfg.WorkflowPolicyResolver,
			FailoverResolver:         cfg.FailoverResolver,
			FailoverPolicy:           cfg.FailoverPolicy,
			UsageLogger:              cfg.UsageLogger,
			PricingResolver:          cfg.PricingResolver,
			TranslatedRequestPatcher: nil,
		}),
	}
}

// ChatCompletion executes one internal translated chat request.
func (e *InternalChatCompletionExecutor) ChatCompletion(ctx context.Context, req *core.ChatRequest) (resp *core.ChatResponse, err error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if req.Stream {
		return nil, core.NewInvalidRequestError("internal translated chat executor does not support streaming requests", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if core.GetRequestOrigin(ctx) != core.RequestOriginPlugin {
		ctx = core.WithRequestOrigin(ctx, core.RequestOriginGuardrail)
	}
	ctx = gateway.WithAttemptRecorder(ctx)

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	requested := core.NewRequestedModelSelector(req.Model, req.Provider)
	start := time.Now()
	entry := e.newAuditEntry(ctx, requestID, requested)
	var workflow *core.Workflow
	var cacheType string
	var meta gateway.ExecutionMeta
	defer func() {
		e.finishAuditEntry(ctx, entry, start, workflow, req, resp, err, cacheType, meta)
	}()

	resolution, err := resolveRequestModelWithAuthorizer(ctx, e.provider, e.modelResolver, e.modelAuthorizer, requested)
	if err != nil {
		return nil, err
	}
	workflow, err = translatedWorkflow(
		ctx,
		requestID,
		core.DescribeEndpoint(http.MethodPost, "/v1/chat/completions"),
		resolution,
		e.workflowPolicyResolver,
	)
	if err != nil {
		return nil, err
	}

	ctx = e.orchestrator.WithCacheRequestContext(ctx, workflow)
	execReq := gateway.CloneChatRequestForSelector(req, resolution.ResolvedSelector)
	resp, meta, cacheType, err = e.executeChatCompletion(ctx, workflow, execReq)
	if err != nil {
		return nil, err
	}

	if cacheType == "" {
		e.orchestrator.LogUsage(ctx, workflow, resp.Model, meta.ProviderType, meta.ProviderName, func(pricing *core.ModelPricing) *usage.UsageEntry {
			return usage.ExtractFromChatResponse(resp, requestID, meta.ProviderType, "/v1/chat/completions", pricing)
		})
	}
	return resp, nil
}

// executeChatCompletion dispatches through the response cache when one is
// configured. The returned cache type is empty for a provider round trip.
func (e *InternalChatCompletionExecutor) executeChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, gateway.ExecutionMeta, string, error) {
	if e.responseCache == nil || (workflow != nil && !workflow.CacheEnabled()) {
		return e.dispatchChatCompletionNoCache(ctx, workflow, req)
	}

	body, err := json.Marshal(req)
	if err != nil {
		slog.Warn("json.Marshal(req) failed; bypassing cache", "err", err)
		return e.dispatchChatCompletionNoCache(ctx, workflow, req)
	}

	var (
		resp *core.ChatResponse
		meta gateway.ExecutionMeta
	)
	result, err := e.responseCache.HandleInternalRequest(ctx, http.MethodPost, "/v1/chat/completions", body, func(callCtx context.Context) (*responsecache.InternalResponse, error) {
		var execErr error
		resp, meta, execErr = e.orchestrator.DispatchChatCompletion(callCtx, workflow, req)
		if execErr != nil {
			return nil, execErr
		}
		respBody, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &responsecache.InternalResponse{
			StatusCode:   http.StatusOK,
			ContentType:  "application/json",
			Body:         respBody,
			FailoverUsed: meta.UsedFailover,
		}, nil
	})
	if err != nil {
		return nil, gateway.ExecutionMeta{}, "", err
	}
	if result != nil && result.CacheType != "" {
		var cached core.ChatResponse
		if err := json.Unmarshal(result.Body, &cached); err != nil {
			return nil, gateway.ExecutionMeta{}, "", err
		}
		cachedMeta := gateway.ExecutionMeta{}
		if workflow != nil {
			cachedMeta.ProviderType = workflow.ProviderType
			cachedMeta.ProviderName = gateway.ProviderNameFromWorkflow(workflow)
		}
		return &cached, cachedMeta, result.CacheType, nil
	}
	return resp, meta, "", nil
}

func (e *InternalChatCompletionExecutor) dispatchChatCompletionNoCache(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, gateway.ExecutionMeta, string, error) {
	resp, meta, err := e.orchestrator.DispatchChatCompletion(ctx, workflow, req)
	return resp, meta, "", err
}

func (e *InternalChatCompletionExecutor) newAuditEntry(
	ctx context.Context,
	requestID string,
	requested core.RequestedModelSelector,
) *auditlog.LogEntry {
	if e.logger == nil || !e.logger.Config().Enabled {
		return nil
	}

	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}

	entry := &auditlog.LogEntry{
		ID:        uuid.NewString(),
		Timestamp: time.Now(),
		RequestID: requestID,
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		UserPath:  userPath,
		SessionID: core.SessionIDFromContext(ctx),
		Data:      &auditlog.LogData{Labels: core.RequestLabelsFromContext(ctx)},
	}
	if requestedModel := requested.RequestedQualifiedModel(); requestedModel != "" {
		entry.RequestedModel = requestedModel
	}
	return entry
}

func (e *InternalChatCompletionExecutor) finishAuditEntry(
	ctx context.Context,
	entry *auditlog.LogEntry,
	start time.Time,
	workflow *core.Workflow,
	req *core.ChatRequest,
	resp *core.ChatResponse,
	err error,
	cacheType string,
	meta gateway.ExecutionMeta,
) {
	if entry == nil || e.logger == nil || !e.logger.Config().Enabled {
		return
	}

	entry.DurationNs = time.Since(start).Nanoseconds()
	auditlog.EnrichLogEntryWithWorkflow(entry, workflow)
	auditlog.EnrichLogEntryWithFailover(entry, meta.FailoverModel)
	auditlog.EnrichLogEntryWithAttempts(entry, auditlog.GateAttemptCapture(auditAttemptsFromGateway(ctx), e.logger.Config()))
	auditlog.EnrichLogEntryWithResolvedRoute(entry, qualifyExecutedModel(workflow, chatResponseModel(resp), meta.ProviderName), meta.ProviderType, meta.ProviderName)
	auditlog.EnrichLogEntryWithRequestContext(entry, ctx)
	if workflow != nil && !workflow.AuditEnabled() {
		return
	}

	cfg := e.logger.Config()
	auditlog.CaptureInternalJSONExchange(entry, ctx, http.MethodPost, "/v1/chat/completions", req, resp, err, cfg)
	if cacheType != "" {
		entry.CacheType = cacheType
	}

	if err != nil {
		var gatewayErr *core.GatewayError
		if errors.As(err, &gatewayErr) && gatewayErr != nil {
			entry.ErrorType = string(gatewayErr.Type)
			entry.StatusCode = gatewayErr.HTTPStatusCode()
			if entry.Data != nil {
				entry.Data.ErrorMessage = gatewayErr.Message
				if gatewayErr.Code != nil {
					entry.Data.ErrorCode = *gatewayErr.Code
				}
			}
		} else {
			entry.ErrorType = string(core.ErrorTypeProvider)
			entry.StatusCode = http.StatusInternalServerError
			if entry.Data != nil {
				entry.Data.ErrorMessage = err.Error()
			}
		}
	} else {
		entry.StatusCode = http.StatusOK
	}

	e.logger.Write(entry)
}

func chatResponseModel(resp *core.ChatResponse) string {
	if resp == nil {
		return ""
	}
	return resp.Model
}
