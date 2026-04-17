package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/usage"
)

// InternalChatCompletionExecutorConfig configures the transport-free translated
// chat execution path used by gateway-owned workflows such as guardrails.
type InternalChatCompletionExecutorConfig struct {
	ModelResolver          RequestModelResolver
	ModelAuthorizer        RequestModelAuthorizer
	WorkflowPolicyResolver RequestWorkflowPolicyResolver
	FallbackResolver       RequestFallbackResolver
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
	service                *translatedInferenceService
	modelAuthorizer        RequestModelAuthorizer
}

// NewInternalChatCompletionExecutor creates a transport-free translated chat
// executor that reuses workflow resolution, fallback, usage, and audit logic.
func NewInternalChatCompletionExecutor(provider core.RoutableProvider, cfg InternalChatCompletionExecutorConfig) *InternalChatCompletionExecutor {
	service := &translatedInferenceService{
		provider:               provider,
		modelResolver:          cfg.ModelResolver,
		modelAuthorizer:        cfg.ModelAuthorizer,
		workflowPolicyResolver: cfg.WorkflowPolicyResolver,
		fallbackResolver:       cfg.FallbackResolver,
		logger:                 cfg.AuditLogger,
		usageLogger:            cfg.UsageLogger,
		pricingResolver:        cfg.PricingResolver,
		responseCache:          cfg.ResponseCache,
	}

	return &InternalChatCompletionExecutor{
		provider:               provider,
		modelResolver:          cfg.ModelResolver,
		modelAuthorizer:        cfg.ModelAuthorizer,
		workflowPolicyResolver: cfg.WorkflowPolicyResolver,
		logger:                 cfg.AuditLogger,
		service:                service,
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
	ctx = core.WithRequestOrigin(ctx, core.RequestOriginGuardrail)

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	requested := core.NewRequestedModelSelector(req.Model, req.Provider)
	start := time.Now()
	entry := e.newAuditEntry(ctx, requestID, requested)
	var workflow *core.Workflow
	var cacheType string
	var providerType string
	var providerName string
	var failoverModel string
	defer func() {
		e.finishAuditEntry(ctx, entry, start, workflow, req, resp, err, cacheType, providerType, providerName, failoverModel)
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

	ctx = e.service.withCacheRequestContext(ctx, workflow)
	execReq := cloneChatRequestForSelector(req, resolution.ResolvedSelector)
	resp, providerType, providerName, failoverModel, _, cacheType, err = e.executeChatCompletion(ctx, workflow, execReq)
	if err != nil {
		return nil, err
	}

	if cacheType == "" {
		e.service.logUsage(ctx, workflow, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
			return usage.ExtractFromChatResponse(resp, requestID, providerType, "/v1/chat/completions", pricing)
		})
	}
	return resp, nil
}

func (e *InternalChatCompletionExecutor) executeChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, string, bool, string, error) {
	if e.service.responseCache == nil || (workflow != nil && !workflow.CacheEnabled()) {
		resp, providerType, providerName, failoverModel, usedFallback, err := e.service.executeChatCompletion(ctx, workflow, req)
		return resp, providerType, providerName, failoverModel, usedFallback, "", err
	}

	body, err := marshalRequestBody(req)
	if err != nil {
		resp, providerType, providerName, failoverModel, usedFallback, execErr := e.service.executeChatCompletion(ctx, workflow, req)
		if execErr != nil {
			return nil, "", "", "", false, "", execErr
		}
		return resp, providerType, providerName, failoverModel, usedFallback, "", nil
	}

	var (
		resp          *core.ChatResponse
		providerType  string
		providerName  string
		failoverModel string
		usedFallback  bool
	)
	result, err := e.service.responseCache.HandleInternalRequest(ctx, http.MethodPost, "/v1/chat/completions", body, func(c *echo.Context) error {
		var execErr error
		resp, providerType, providerName, failoverModel, usedFallback, execErr = e.service.executeChatCompletion(c.Request().Context(), workflow, req)
		if execErr != nil {
			return execErr
		}
		if usedFallback {
			c.SetRequest(c.Request().WithContext(core.WithFallbackUsed(c.Request().Context())))
		}
		return c.JSON(http.StatusOK, resp)
	})
	if err != nil {
		return nil, "", "", "", false, "", err
	}
	if result != nil && result.CacheType != "" {
		var cached core.ChatResponse
		if err := json.Unmarshal(result.Body, &cached); err != nil {
			return nil, "", "", "", false, "", err
		}
		return &cached, workflow.ProviderType, providerNameFromWorkflow(workflow), "", false, result.CacheType, nil
	}
	return resp, providerType, providerName, failoverModel, usedFallback, "", nil
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
		Data:      &auditlog.LogData{},
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
	providerType string,
	providerName string,
	failoverModel string,
) {
	if entry == nil || e.logger == nil || !e.logger.Config().Enabled {
		return
	}

	entry.DurationNs = time.Since(start).Nanoseconds()
	auditlog.EnrichLogEntryWithWorkflow(entry, workflow)
	auditlog.EnrichLogEntryWithFailover(entry, failoverModel)
	auditlog.EnrichLogEntryWithResolvedRoute(entry, qualifyExecutedModel(workflow, chatResponseModel(resp), providerName), providerType, providerName)
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
