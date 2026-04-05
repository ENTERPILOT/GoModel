package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/usage"
)

// InternalChatCompletionExecutorConfig configures an in-process translated chat
// execution path used by gateway-owned workflows such as guardrails.
type InternalChatCompletionExecutorConfig struct {
	ModelResolver            RequestModelResolver
	ExecutionPolicyResolver  RequestExecutionPolicyResolver
	FallbackResolver         RequestFallbackResolver
	TranslatedRequestPatcher TranslatedRequestPatcher
	AuditLogger              auditlog.LoggerInterface
	UsageLogger              usage.LoggerInterface
	PricingResolver          usage.PricingResolver
	ResponseCacheMiddleware  *responsecache.ResponseCacheMiddleware
}

// InternalChatCompletionExecutor executes synthetic translated chat requests
// through the same in-process routing stack as external traffic.
type InternalChatCompletionExecutor struct {
	echo  *echo.Echo
	chain echo.HandlerFunc
}

// NewInternalChatCompletionExecutor creates an in-process translated chat
// executor that reuses request planning, fallback, usage, audit, and cache.
func NewInternalChatCompletionExecutor(provider core.RoutableProvider, cfg InternalChatCompletionExecutorConfig) *InternalChatCompletionExecutor {
	handler := newHandler(
		provider,
		cfg.AuditLogger,
		cfg.UsageLogger,
		cfg.PricingResolver,
		cfg.ModelResolver,
		cfg.ExecutionPolicyResolver,
		cfg.FallbackResolver,
		skipTranslatedRequestPatchingForGuardrailOrigin(cfg.TranslatedRequestPatcher),
	)
	handler.responseCache = cfg.ResponseCacheMiddleware

	chain := handler.ChatCompletion
	chain = ExecutionPlanningWithResolverAndPolicy(provider, cfg.ModelResolver, cfg.ExecutionPolicyResolver)(chain)
	if cfg.AuditLogger != nil && cfg.AuditLogger.Config().Enabled {
		chain = auditlog.Middleware(cfg.AuditLogger)(chain)
	}
	chain = RequestSnapshotCapture()(chain)

	return &InternalChatCompletionExecutor{
		echo:  echo.New(),
		chain: chain,
	}
}

type guardrailOriginSkippingPatcher struct {
	inner TranslatedRequestPatcher
}

func (p guardrailOriginSkippingPatcher) PatchChatRequest(ctx context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	if core.GetRequestOrigin(ctx) == core.RequestOriginGuardrail {
		return req, nil
	}
	return p.inner.PatchChatRequest(ctx, req)
}

func (p guardrailOriginSkippingPatcher) PatchResponsesRequest(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	if core.GetRequestOrigin(ctx) == core.RequestOriginGuardrail {
		return req, nil
	}
	return p.inner.PatchResponsesRequest(ctx, req)
}

func skipTranslatedRequestPatchingForGuardrailOrigin(patcher TranslatedRequestPatcher) TranslatedRequestPatcher {
	if patcher == nil {
		return nil
	}
	return guardrailOriginSkippingPatcher{inner: patcher}
}

// ChatCompletion executes one synthetic translated chat request in-process.
func (e *InternalChatCompletionExecutor) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if req.Stream {
		return nil, core.NewInvalidRequestError("internal translated chat executor does not support streaming requests", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = core.WithoutRequestExecutionState(ctx)
	ctx = core.WithRequestOrigin(ctx, core.RequestOriginGuardrail)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode internal chat request", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	if requestID := strings.TrimSpace(core.GetRequestID(ctx)); requestID != "" {
		httpReq.Header.Set("X-Request-ID", requestID)
	}
	if userPath := strings.TrimSpace(core.UserPathFromContext(ctx)); userPath != "" {
		httpReq.Header.Set(core.UserPathHeader, userPath)
	}
	copyTraceHeaders(httpReq.Header, core.GetRequestSnapshot(ctx))

	rec := httptest.NewRecorder()
	c := e.echo.NewContext(httpReq, rec)
	c.SetPath("/v1/chat/completions")

	if err := e.chain(c); err != nil {
		return nil, err
	}

	if rec.Code >= http.StatusBadRequest {
		return nil, decodeInternalExecutorError(rec.Code, rec.Body.Bytes())
	}

	var resp core.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return nil, core.NewProviderError("", http.StatusBadGateway, "failed to decode internal chat response", err)
	}
	return &resp, nil
}

func copyTraceHeaders(dst http.Header, snapshot *core.RequestSnapshot) {
	if dst == nil || snapshot == nil {
		return
	}
	headers := snapshot.GetHeaders()
	for _, key := range []string{"Traceparent", "Tracestate", "Baggage"} {
		values := headers[key]
		if len(values) == 0 {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func decodeInternalExecutorError(statusCode int, body []byte) error {
	var payload struct {
		Error struct {
			Type    core.ErrorType `json:"type"`
			Message string         `json:"message"`
			Param   *string        `json:"param"`
			Code    *string        `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return &core.GatewayError{
			Type:       payload.Error.Type,
			Message:    payload.Error.Message,
			StatusCode: statusCode,
			Param:      payload.Error.Param,
			Code:       payload.Error.Code,
		}
	}
	return core.NewProviderError("", statusCode, strings.TrimSpace(string(body)), nil)
}
