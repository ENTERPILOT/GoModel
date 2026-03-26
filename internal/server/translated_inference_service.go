package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

// translatedInferenceService owns translated chat/responses/embeddings
// execution so HTTP handlers can stay focused on transport concerns.
type translatedInferenceService struct {
	provider                 core.RoutableProvider
	modelResolver            RequestModelResolver
	executionPolicyResolver  RequestExecutionPolicyResolver
	translatedRequestPatcher TranslatedRequestPatcher
	logger                   auditlog.LoggerInterface
	usageLogger              usage.LoggerInterface
	pricingResolver          usage.PricingResolver
	responseCache            *responsecache.ResponseCacheMiddleware
	guardrailsHash           string

	// Pre-built handlers initialized via initHandlers.
	chatCompletionHandler echo.HandlerFunc
	responsesHandler      echo.HandlerFunc
}

func (s *translatedInferenceService) initHandlers() {
	s.chatCompletionHandler = newTranslatedHandler(s,
		core.DecodeChatRequest,
		func(r *core.ChatRequest) (*string, *string) { return &r.Model, &r.Provider },
		func(ctx context.Context, r *core.ChatRequest) (*core.ChatRequest, error) {
			return s.translatedRequestPatcher.PatchChatRequest(ctx, r)
		},
		func(r *core.ChatRequest) bool { return r.Stream },
		s.dispatchChatCompletion,
	)
	s.responsesHandler = newTranslatedHandler(s,
		core.DecodeResponsesRequest,
		func(r *core.ResponsesRequest) (*string, *string) { return &r.Model, &r.Provider },
		func(ctx context.Context, r *core.ResponsesRequest) (*core.ResponsesRequest, error) {
			return s.translatedRequestPatcher.PatchResponsesRequest(ctx, r)
		},
		func(r *core.ResponsesRequest) bool { return r.Stream },
		s.dispatchResponses,
	)
}

// newTranslatedHandler returns an echo.HandlerFunc that executes the
// decode→plan→patch→dispatch pipeline for a translated inference endpoint.
func newTranslatedHandler[R any](
	s *translatedInferenceService,
	decode func([]byte, *core.WhiteBoxPrompt) (R, error),
	modelProvider func(R) (*string, *string),
	patch func(context.Context, R) (R, error),
	isStream func(R) bool,
	dispatch func(*echo.Context, R, *core.ExecutionPlan) error,
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		return handleTranslatedInference(s, c, decode, modelProvider, patch, isStream, dispatch)
	}
}

func (s *translatedInferenceService) ChatCompletion(c *echo.Context) error {
	return s.chatCompletionHandler(c)
}

func (s *translatedInferenceService) dispatchChatCompletion(c *echo.Context, req *core.ChatRequest, plan *core.ExecutionPlan) error {
	ctx := c.Request().Context()
	streamReq, providerType, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, req)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		return s.handleStreamingResponse(c, plan, usageModel, providerType, func() (io.ReadCloser, error) {
			return s.provider.StreamChatCompletion(ctx, streamReq)
		})
	}

	resp, err := s.provider.ChatCompletion(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(plan, resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, "/v1/chat/completions", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) Responses(c *echo.Context) error {
	return s.responsesHandler(c)
}

// handleTranslatedInference is the shared decode→plan→patch→dispatch pipeline
// for ChatCompletion and Responses, parameterised over the request type.
func handleTranslatedInference[R any](
	s *translatedInferenceService,
	c *echo.Context,
	decode func([]byte, *core.WhiteBoxPrompt) (R, error),
	modelProvider func(R) (*string, *string),
	patch func(context.Context, R) (R, error),
	isStream func(R) bool,
	dispatch func(*echo.Context, R, *core.ExecutionPlan) error,
) error {
	req, err := canonicalJSONRequestFromSemantics(c, decode)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	modelPtr, providerPtr := modelProvider(req)
	plan, err := ensureTranslatedRequestPlan(c, s.provider, s.modelResolver, s.executionPolicyResolver, modelPtr, providerPtr)
	if err != nil {
		return handleError(c, err)
	}

	if s.translatedRequestPatcher != nil {
		ctx := c.Request().Context()
		req, err = patch(ctx, req)
		if err != nil {
			return handleError(c, err)
		}
	}

	return handleWithCache(s, c, req, isStream(req), plan, dispatch)
}

// handleWithCache injects the guardrails hash into context, then either routes the
// request through the dual-layer response cache (non-streaming) or calls dispatch
// directly (streaming). R is the post-patch request type.
func handleWithCache[R any](
	s *translatedInferenceService,
	c *echo.Context,
	req R,
	stream bool,
	plan *core.ExecutionPlan,
	dispatch func(*echo.Context, R, *core.ExecutionPlan) error,
) error {
	guardrailsHash := s.guardrailsHash
	if plan != nil && plan.Policy != nil {
		guardrailsHash = plan.GuardrailsHash()
	}
	if guardrailsHash != "" {
		ctx := core.WithGuardrailsHash(c.Request().Context(), guardrailsHash)
		c.SetRequest(c.Request().WithContext(ctx))
	}

	if s.responseCache != nil && !stream && (plan == nil || plan.CacheEnabled()) {
		body, marshalErr := marshalRequestBody(req)
		if marshalErr != nil {
			slog.Debug("marshalRequestBody failed", "err", marshalErr)
		} else {
			return s.responseCache.HandleRequest(c, body, func() error {
				return dispatch(c, req, plan)
			})
		}
	}

	return dispatch(c, req, plan)
}

func (s *translatedInferenceService) dispatchResponses(c *echo.Context, req *core.ResponsesRequest, plan *core.ExecutionPlan) error {
	ctx := c.Request().Context()
	_, providerType, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if (plan == nil || plan.UsageEnabled()) && s.shouldEnforceReturningUsageData() {
			ctx = core.WithEnforceReturningUsageData(ctx, true)
		}
		return s.handleStreamingResponse(c, plan, usageModel, providerType, func() (io.ReadCloser, error) {
			return s.provider.StreamResponses(ctx, req)
		})
	}

	resp, err := s.provider.Responses(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(plan, resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, "/v1/responses", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) Embeddings(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.EmbeddingRequest](c, core.DecodeEmbeddingRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	plan, err := ensureTranslatedRequestPlan(c, s.provider, s.modelResolver, s.executionPolicyResolver, &req.Model, &req.Provider)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()
	_, providerType, _ := s.resolveProviderAndModelFromPlan(c, plan, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	resp, err := s.provider.Embeddings(ctx, req)
	if err != nil {
		return handleError(c, err)
	}

	s.logUsage(plan, resp.Model, providerType, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, "/v1/embeddings", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) handleStreamingResponse(
	c *echo.Context,
	plan *core.ExecutionPlan,
	model, provider string,
	streamFn func() (io.ReadCloser, error),
) error {
	stream, err := streamFn()
	if err != nil {
		return handleError(c, err)
	}

	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)

	entry := auditlog.GetStreamEntryFromContext(c)
	streamEntry := auditlog.CreateStreamEntry(entry)
	if streamEntry != nil {
		streamEntry.StatusCode = http.StatusOK
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	endpoint := c.Request().URL.Path
	observers := make([]streaming.Observer, 0, 2)
	if s.logger != nil && s.logger.Config().Enabled && streamEntry != nil && (plan == nil || plan.AuditEnabled()) {
		observers = append(observers, auditlog.NewStreamLogObserver(s.logger, streamEntry, endpoint))
	}
	if s.usageLogger != nil && s.usageLogger.Config().Enabled && (plan == nil || plan.UsageEnabled()) {
		observers = append(observers, usage.NewStreamUsageObserver(s.usageLogger, model, provider, requestID, endpoint, s.pricingResolver))
	}
	wrappedStream := streaming.NewObservedSSEStream(stream, observers...)

	defer func() {
		_ = wrappedStream.Close() //nolint:errcheck
	}()

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

	if streamEntry != nil && streamEntry.Data != nil {
		streamEntry.Data.ResponseHeaders = map[string]string{
			"Content-Type":  "text/event-stream",
			"Cache-Control": "no-cache",
			"Connection":    "keep-alive",
		}
	}

	c.Response().WriteHeader(http.StatusOK)
	if err := flushStream(c.Response(), wrappedStream); err != nil {
		recordStreamingError(streamEntry, model, provider, c.Request().URL.Path, requestID, err)
	}
	return nil
}

func (s *translatedInferenceService) logUsage(
	plan *core.ExecutionPlan,
	model, providerType string,
	extractFn func(*core.ModelPricing) *usage.UsageEntry,
) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled || (plan != nil && !plan.UsageEnabled()) {
		return
	}
	var pricing *core.ModelPricing
	if s.pricingResolver != nil {
		pricing = s.pricingResolver.ResolvePricing(model, providerType)
	}
	if entry := extractFn(pricing); entry != nil {
		s.usageLogger.Write(entry)
	}
}

func (s *translatedInferenceService) shouldEnforceReturningUsageData() bool {
	return s.usageLogger != nil && s.usageLogger.Config().EnforceReturningUsageData
}

func (s *translatedInferenceService) resolveProviderAndModelFromPlan(
	c *echo.Context,
	plan *core.ExecutionPlan,
	fallbackModel string,
	req *core.ChatRequest,
) (*core.ChatRequest, string, string) {
	providerType := GetProviderType(c)
	if plan != nil {
		if plannedProviderType := strings.TrimSpace(plan.ProviderType); plannedProviderType != "" {
			providerType = plannedProviderType
		}
	}

	model := resolvedModelFromPlan(plan, fallbackModel)
	if req == nil || !req.Stream || (plan != nil && !plan.UsageEnabled()) || !s.shouldEnforceReturningUsageData() {
		return req, providerType, model
	}

	streamReq := cloneChatRequestForStreamUsage(req)
	if streamReq.StreamOptions == nil {
		streamReq.StreamOptions = &core.StreamOptions{}
	}
	streamReq.StreamOptions.IncludeUsage = true
	return streamReq, providerType, model
}

func recordStreamingError(streamEntry *auditlog.LogEntry, model, provider, path, requestID string, err error) {
	if streamEntry != nil {
		streamEntry.ErrorType = "stream_error"
		if streamEntry.Data == nil {
			streamEntry.Data = &auditlog.LogData{}
		}
		streamEntry.Data.ErrorMessage = err.Error()
	}

	slog.Warn("stream terminated abnormally",
		"error", err,
		"model", model,
		"provider", provider,
		"path", path,
		"request_id", requestID,
	)
}

func cloneChatRequestForStreamUsage(req *core.ChatRequest) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	return &cloned
}

func resolvedModelFromPlan(plan *core.ExecutionPlan, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if plan == nil || plan.Resolution == nil {
		return fallback
	}
	if resolvedModel := strings.TrimSpace(plan.Resolution.ResolvedSelector.Model); resolvedModel != "" {
		return resolvedModel
	}
	return fallback
}

// marshalRequestBody serializes a patched request struct to JSON bytes for cache key computation.
// Returns an error only on marshalling failure; callers bypass cache on error.
func marshalRequestBody(req any) ([]byte, error) {
	return json.Marshal(req)
}
