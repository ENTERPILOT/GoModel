package server

import (
	"context"
	"encoding/json"
	"errors"
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
	fallbackResolver         RequestFallbackResolver
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
	streamReq, providerType, providerName, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, req)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if len(s.fallbackSelectors(plan)) == 0 {
			if handled, err := s.tryFastPathStreamingChatPassthrough(c, plan, req); handled {
				return err
			}
		}
		stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, usedFallback, err := s.streamChatCompletion(ctx, plan, streamReq, providerType, providerName, usageModel)
		if err != nil {
			return handleError(c, err)
		}
		if usedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(c, plan, resolvedUsageModel, resolvedProviderType, resolvedProviderName, stream)
	}

	resp, providerType, providerName, usedFallback, err := s.executeChatCompletion(ctx, plan, req)
	if err != nil {
		return handleError(c, err)
	}
	if usedFallback {
		markRequestFallbackUsed(c)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(plan, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, plan, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
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

// handleWithCache injects the guardrails hash into context, then routes the
// request through the dual-layer response cache when caching is enabled.
// Streaming requests are stored as full responses and replayed as SSE on hits.
// R is the post-patch request type.
func handleWithCache[R any](
	s *translatedInferenceService,
	c *echo.Context,
	req R,
	stream bool,
	plan *core.ExecutionPlan,
	dispatch func(*echo.Context, R, *core.ExecutionPlan) error,
) error {
	ctx := s.withCacheRequestContext(c.Request().Context(), plan)
	c.SetRequest(c.Request().WithContext(ctx))

	if s.responseCache != nil && (plan == nil || plan.CacheEnabled()) {
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

func (s *translatedInferenceService) withCacheRequestContext(ctx context.Context, plan *core.ExecutionPlan) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if plan != nil {
		ctx = core.WithExecutionPlan(ctx, plan)
	}
	if plan != nil && plan.Policy != nil {
		return core.WithGuardrailsHash(ctx, plan.GuardrailsHash())
	}
	if s.guardrailsHash != "" {
		return core.WithGuardrailsHash(ctx, s.guardrailsHash)
	}
	return ctx
}

func (s *translatedInferenceService) dispatchResponses(c *echo.Context, req *core.ResponsesRequest, plan *core.ExecutionPlan) error {
	ctx := c.Request().Context()
	_, providerType, providerName, usageModel := s.resolveProviderAndModelFromPlan(c, plan, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if (plan == nil || plan.UsageEnabled()) && s.shouldEnforceReturningUsageData() {
			ctx = core.WithEnforceReturningUsageData(ctx, true)
		}
		stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, usedFallback, err := s.streamResponses(ctx, plan, req, providerType, providerName, usageModel)
		if err != nil {
			return handleError(c, err)
		}
		if usedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(c, plan, resolvedUsageModel, resolvedProviderType, resolvedProviderName, stream)
	}

	resp, providerType, providerName, usedFallback, err := s.executeResponses(ctx, plan, req)
	if err != nil {
		return handleError(c, err)
	}
	if usedFallback {
		markRequestFallbackUsed(c)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(plan, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, plan, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, "/v1/responses", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) tryFastPathStreamingChatPassthrough(c *echo.Context, plan *core.ExecutionPlan, req *core.ChatRequest) (bool, error) {
	if !s.canFastPathStreamingChatPassthrough(plan, req) {
		return false, nil
	}

	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return false, nil
	}

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))

	const endpoint = "/chat/completions"
	providerType := strings.TrimSpace(plan.ProviderType)
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:   c.Request().Method,
		Endpoint: endpoint,
		Body:     c.Request().Body,
		Headers:  buildPassthroughHeaders(ctx, c.Request().Header),
	})
	if err != nil {
		return true, handleError(c, err)
	}

	info := &core.PassthroughRouteInfo{
		Provider:    providerType,
		RawEndpoint: strings.TrimPrefix(endpoint, "/"),
		AuditPath:   c.Request().URL.Path,
		Model:       resolvedModelFromPlan(plan, req.Model),
	}
	passthrough := passthroughService{
		provider:        s.provider,
		logger:          s.logger,
		usageLogger:     s.usageLogger,
		pricingResolver: s.pricingResolver,
	}
	return true, passthrough.proxyPassthroughResponse(c, providerType, providerNameFromPlan(plan), endpoint, info, resp)
}

func (s *translatedInferenceService) canFastPathStreamingChatPassthrough(plan *core.ExecutionPlan, req *core.ChatRequest) bool {
	if req == nil || !req.Stream {
		return false
	}
	if s.translatedRequestPatcher != nil || s.shouldEnforceReturningUsageData() {
		return false
	}
	if plan == nil || plan.Resolution == nil {
		return false
	}

	providerType := strings.ToLower(strings.TrimSpace(plan.ProviderType))
	switch providerType {
	case "openai", "azure", "openrouter":
	default:
		return false
	}

	if translatedStreamingSelectorRewriteRequired(plan.Resolution) {
		return false
	}
	if translatedStreamingChatBodyRewriteRequired(req) {
		return false
	}

	return true
}

func translatedStreamingSelectorRewriteRequired(resolution *core.RequestModelResolution) bool {
	if resolution == nil {
		return true
	}

	requestedModel := strings.TrimSpace(resolution.Requested.Model)
	requestedProvider := strings.TrimSpace(resolution.Requested.ProviderHint)
	resolvedModel := strings.TrimSpace(resolution.ResolvedSelector.Model)
	resolvedProvider := strings.TrimSpace(resolution.ResolvedSelector.Provider)

	return requestedModel != resolvedModel || requestedProvider != resolvedProvider
}

func translatedStreamingChatBodyRewriteRequired(req *core.ChatRequest) bool {
	if req == nil {
		return true
	}
	if strings.TrimSpace(req.Provider) != "" {
		return true
	}

	model := strings.ToLower(strings.TrimSpace(req.Model))
	oSeries := len(model) >= 2 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9'
	return oSeries && (req.MaxTokens != nil || req.Temperature != nil)
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
	requestID := requestIDFromContextOrHeader(c.Request())

	resp, providerType, providerName, err := s.executeEmbeddings(ctx, plan, req)
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(plan, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, plan, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, "/v1/embeddings", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) handleStreamingReadCloser(
	c *echo.Context,
	plan *core.ExecutionPlan,
	model, provider, providerName string,
	stream io.ReadCloser,
) error {
	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(plan, model, providerName), provider, providerName)

	entry := auditlog.GetStreamEntryFromContext(c)
	auditEnabled := s.logger != nil && s.logger.Config().Enabled && (plan == nil || plan.AuditEnabled())
	if auditEnabled && entry != nil {
		auditlog.PopulateRequestData(entry, c.Request(), s.logger.Config())
	}
	streamEntry := auditlog.CreateStreamEntry(entry)
	if streamEntry != nil {
		streamEntry.StatusCode = http.StatusOK
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	endpoint := c.Request().URL.Path
	observers := make([]streaming.Observer, 0, 2)
	if auditEnabled && streamEntry != nil {
		observers = append(observers, auditlog.NewStreamLogObserver(s.logger, streamEntry, endpoint))
	}
	if s.usageLogger != nil && s.usageLogger.Config().Enabled && (plan == nil || plan.UsageEnabled()) {
		usageObserver := usage.NewStreamUsageObserver(s.usageLogger, model, provider, requestID, endpoint, s.pricingResolver, core.UserPathFromContext(c.Request().Context()))
		if usageObserver != nil {
			usageObserver.SetProviderName(providerName)
			observers = append(observers, usageObserver)
		}
	}
	wrappedStream := streaming.NewObservedSSEStream(stream, observers...)

	defer func() {
		_ = wrappedStream.Close() //nolint:errcheck
	}()

	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

	if auditEnabled && streamEntry != nil && s.logger.Config().LogHeaders {
		auditlog.PopulateResponseHeaders(streamEntry, c.Response().Header())
	}

	c.Response().WriteHeader(http.StatusOK)
	if err := flushStream(c.Response(), wrappedStream); err != nil {
		recordStreamingError(streamEntry, model, provider, c.Request().URL.Path, requestID, err)
	}
	return nil
}

func (s *translatedInferenceService) handleStreamingResponse(
	c *echo.Context,
	plan *core.ExecutionPlan,
	model, provider, providerName string,
	streamFn func() (io.ReadCloser, error),
) error {
	stream, err := streamFn()
	if err != nil {
		return handleError(c, err)
	}
	return s.handleStreamingReadCloser(c, plan, model, provider, providerName, stream)
}

//nolint:dupl // typed wrapper over the shared translated fallback executor
func (s *translatedInferenceService) executeChatCompletion(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, bool, error) {
	return executeTranslatedWithFallback(ctx, s, plan, req, req.Model, req.Provider, cloneChatRequestForSelector,
		func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, string, error) {
			resp, err := s.provider.ChatCompletion(ctx, req)
			if err != nil {
				return nil, "", err
			}
			return resp, resp.Provider, nil
		},
	)
}

func (s *translatedInferenceService) streamChatCompletion(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.ChatRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, bool, error) {
	stream, err := s.provider.StreamChatCompletion(ctx, req)
	if err == nil {
		return stream, providerType, providerName, usageModel, false, nil
	}

	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, err := tryFallbackStream(ctx, s, plan, req.Model, req.Provider, err,
		func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error) {
			stream, err := s.provider.StreamChatCompletion(ctx, cloneChatRequestForSelector(req, selector))
			if err != nil {
				return nil, "", "", err
			}
			return stream, providerType, selector.Model, nil
		},
	)
	if err != nil {
		return nil, "", "", "", false, err
	}
	return stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, true, nil
}

//nolint:dupl // typed wrapper over the shared translated fallback executor
func (s *translatedInferenceService) executeResponses(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, string, string, bool, error) {
	return executeTranslatedWithFallback(ctx, s, plan, req, req.Model, req.Provider, cloneResponsesRequestForSelector,
		func(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, string, error) {
			resp, err := s.provider.Responses(ctx, req)
			if err != nil {
				return nil, "", err
			}
			return resp, resp.Provider, nil
		},
	)
}

func (s *translatedInferenceService) streamResponses(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.ResponsesRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, bool, error) {
	stream, err := s.provider.StreamResponses(ctx, req)
	if err == nil {
		return stream, providerType, providerName, usageModel, false, nil
	}

	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, err := tryFallbackStream(ctx, s, plan, req.Model, req.Provider, err,
		func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error) {
			stream, err := s.provider.StreamResponses(ctx, cloneResponsesRequestForSelector(req, selector))
			if err != nil {
				return nil, "", "", err
			}
			return stream, providerType, selector.Model, nil
		},
	)
	if err != nil {
		return nil, "", "", "", false, err
	}
	return stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, true, nil
}

func (s *translatedInferenceService) executeEmbeddings(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.EmbeddingRequest,
) (*core.EmbeddingResponse, string, string, error) {
	providerType := providerTypeFromPlan(plan)
	providerName := providerNameFromPlan(plan)
	resp, err := s.provider.Embeddings(ctx, req)
	if err == nil {
		return resp, responseProviderType(providerType, resp.Provider), providerName, nil
	}

	return s.tryFallbackEmbeddings(ctx, plan, req, err)
}

func (s *translatedInferenceService) tryFallbackEmbeddings(
	ctx context.Context,
	plan *core.ExecutionPlan,
	req *core.EmbeddingRequest,
	primaryErr error,
) (*core.EmbeddingResponse, string, string, error) {
	// Embeddings fallback is intentionally disabled until the shared model
	// contract can prove vector-size compatibility for alternates.
	return nil, "", "", primaryErr
}

func (s *translatedInferenceService) logUsage(
	ctx context.Context,
	plan *core.ExecutionPlan,
	model, providerType, providerName string,
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
		entry.ProviderName = strings.TrimSpace(providerName)
		entry.UserPath = core.UserPathFromContext(ctx)
		s.usageLogger.Write(entry)
	}
}

func (s *translatedInferenceService) shouldEnforceReturningUsageData() bool {
	return s.usageLogger != nil && s.usageLogger.Config().EnforceReturningUsageData
}

func (s *translatedInferenceService) fallbackSelectors(plan *core.ExecutionPlan) []core.ModelSelector {
	if s.fallbackResolver == nil || plan == nil || plan.Resolution == nil || !plan.FallbackEnabled() {
		return nil
	}
	return s.fallbackResolver.ResolveFallbacks(plan.Resolution, plan.Endpoint.Operation)
}

func (s *translatedInferenceService) providerTypeForSelector(selector core.ModelSelector, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if s.provider == nil {
		if provider := strings.TrimSpace(selector.Provider); provider != "" {
			return provider
		}
		return fallback
	}
	if providerType := strings.TrimSpace(s.provider.GetProviderType(selector.QualifiedModel())); providerType != "" {
		return providerType
	}
	if provider := strings.TrimSpace(selector.Provider); provider != "" {
		return provider
	}
	return fallback
}

func (s *translatedInferenceService) resolveProviderAndModelFromPlan(
	c *echo.Context,
	plan *core.ExecutionPlan,
	fallbackModel string,
	req *core.ChatRequest,
) (*core.ChatRequest, string, string, string) {
	providerType := GetProviderType(c)
	providerName := providerNameFromPlan(plan)
	if plan != nil {
		if plannedProviderType := strings.TrimSpace(plan.ProviderType); plannedProviderType != "" {
			providerType = plannedProviderType
		}
	}

	model := resolvedModelFromPlan(plan, fallbackModel)
	if req == nil || !req.Stream || (plan != nil && !plan.UsageEnabled()) || !s.shouldEnforceReturningUsageData() {
		return req, providerType, providerName, model
	}

	streamReq := cloneChatRequestForStreamUsage(req)
	if streamReq.StreamOptions == nil {
		streamReq.StreamOptions = &core.StreamOptions{}
	}
	streamReq.StreamOptions.IncludeUsage = true
	return streamReq, providerType, providerName, model
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

func cloneChatRequestForSelector(req *core.ChatRequest, selector core.ModelSelector) *core.ChatRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Model = selector.Model
	cloned.Provider = selector.Provider
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	return &cloned
}

func cloneResponsesRequestForSelector(req *core.ResponsesRequest, selector core.ModelSelector) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	cloned := *req
	cloned.Model = selector.Model
	cloned.Provider = selector.Provider
	if req.StreamOptions != nil {
		streamOptions := *req.StreamOptions
		cloned.StreamOptions = &streamOptions
	}
	return &cloned
}

func providerNameFromPlan(plan *core.ExecutionPlan) string {
	if plan == nil || plan.Resolution == nil {
		return ""
	}
	return strings.TrimSpace(plan.Resolution.ProviderName)
}

func resolvedModelPrefix(plan *core.ExecutionPlan, providerName string) string {
	if providerName = strings.TrimSpace(providerName); providerName != "" {
		return providerName
	}
	if plan == nil || plan.Resolution == nil {
		return ""
	}
	if providerName = strings.TrimSpace(plan.Resolution.ProviderName); providerName != "" {
		return providerName
	}
	return strings.TrimSpace(plan.Resolution.ResolvedSelector.Provider)
}

func qualifyModelWithProvider(model, providerName string) string {
	model = strings.TrimSpace(model)
	providerName = strings.TrimSpace(providerName)
	if model == "" {
		return ""
	}
	if providerName == "" || strings.HasPrefix(model, providerName+"/") {
		return model
	}
	return providerName + "/" + model
}

func qualifyExecutedModel(plan *core.ExecutionPlan, model, providerName string) string {
	return qualifyModelWithProvider(model, resolvedModelPrefix(plan, providerName))
}

func markRequestFallbackUsed(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	c.SetRequest(c.Request().WithContext(core.WithFallbackUsed(c.Request().Context())))
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

func providerTypeFromPlan(plan *core.ExecutionPlan) string {
	if plan == nil {
		return ""
	}
	return strings.TrimSpace(plan.ProviderType)
}

func currentSelectorForPlan(plan *core.ExecutionPlan, model, provider string) string {
	if plan != nil && plan.Resolution != nil {
		if resolved := strings.TrimSpace(plan.Resolution.ResolvedQualifiedModel()); resolved != "" {
			return resolved
		}
	}
	selector, err := core.ParseModelSelector(model, provider)
	if err != nil {
		return strings.TrimSpace(model)
	}
	return selector.QualifiedModel()
}

func responseProviderType(fallback, responseProvider string) string {
	responseProvider = strings.TrimSpace(responseProvider)
	if responseProvider != "" {
		return responseProvider
	}
	return strings.TrimSpace(fallback)
}

func tryFallbackResponse[T any](
	ctx context.Context,
	s *translatedInferenceService,
	plan *core.ExecutionPlan,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, bool, error) {
	var zero T

	fallbacks := s.fallbackSelectors(plan)
	if len(fallbacks) == 0 || !shouldAttemptFallback(primaryErr) {
		return zero, "", "", false, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForPlan(plan, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		qualified := selector.QualifiedModel()
		providerType := s.providerTypeForSelector(selector, providerTypeFromPlan(plan))
		providerName := resolvedProviderName(s.provider, selector, providerNameFromPlan(plan))
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
			return resp, resolvedProviderType, providerName, true, nil
		}
		lastErr = err
	}

	return zero, "", "", false, lastErr
}

func executeWithFallbackResponse[T any](
	ctx context.Context,
	s *translatedInferenceService,
	plan *core.ExecutionPlan,
	model, provider string,
	primary func() (T, string, string, error),
	fallback func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, bool, error) {
	resp, resolvedProviderType, resolvedProviderName, err := primary()
	if err == nil {
		return resp, resolvedProviderType, resolvedProviderName, false, nil
	}
	return tryFallbackResponse(ctx, s, plan, model, provider, err, fallback)
}

func executeTranslatedWithFallback[Req any, Resp any](
	ctx context.Context,
	s *translatedInferenceService,
	plan *core.ExecutionPlan,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (Resp, string, error),
) (Resp, string, string, bool, error) {
	return executeWithFallbackResponse(ctx, s, plan, model, provider,
		func() (Resp, string, string, error) {
			resp, responseProvider, err := call(ctx, req)
			if err != nil {
				var zero Resp
				return zero, "", "", err
			}
			return resp, responseProviderType(providerTypeFromPlan(plan), responseProvider), providerNameFromPlan(plan), nil
		},
		func(selector core.ModelSelector, providerType, providerName string) (Resp, string, error) {
			resp, responseProvider, err := call(ctx, cloneForSelector(req, selector))
			if err != nil {
				var zero Resp
				return zero, "", err
			}
			return resp, responseProviderType(providerType, responseProvider), nil
		},
	)
}

func tryFallbackStream(
	ctx context.Context,
	s *translatedInferenceService,
	plan *core.ExecutionPlan,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error),
) (io.ReadCloser, string, string, string, error) {
	fallbacks := s.fallbackSelectors(plan)
	if len(fallbacks) == 0 || !shouldAttemptFallback(primaryErr) {
		return nil, "", "", "", primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForPlan(plan, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		qualified := selector.QualifiedModel()
		providerType := s.providerTypeForSelector(selector, providerTypeFromPlan(plan))
		providerName := resolvedProviderName(s.provider, selector, providerNameFromPlan(plan))
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
			return stream, resolvedProviderType, providerName, usageModel, nil
		}
		lastErr = err
	}

	return nil, "", "", "", lastErr
}

func shouldAttemptFallback(err error) bool {
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
