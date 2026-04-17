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
	modelAuthorizer          RequestModelAuthorizer
	workflowPolicyResolver   RequestWorkflowPolicyResolver
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
// decode→workflow→patch→dispatch pipeline for a translated inference endpoint.
func newTranslatedHandler[R any](
	s *translatedInferenceService,
	decode func([]byte, *core.WhiteBoxPrompt) (R, error),
	modelProvider func(R) (*string, *string),
	patch func(context.Context, R) (R, error),
	isStream func(R) bool,
	dispatch func(*echo.Context, R, *core.Workflow) error,
) echo.HandlerFunc {
	return func(c *echo.Context) error {
		return handleTranslatedInference(s, c, decode, modelProvider, patch, isStream, dispatch)
	}
}

func (s *translatedInferenceService) ChatCompletion(c *echo.Context) error {
	return s.chatCompletionHandler(c)
}

func (s *translatedInferenceService) dispatchChatCompletion(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow) error {
	ctx := c.Request().Context()
	streamReq, providerType, providerName, usageModel := s.resolveProviderAndModelFromWorkflow(c, workflow, req.Model, req)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if len(s.fallbackSelectors(workflow)) == 0 {
			if handled, err := s.tryFastPathStreamingChatPassthrough(c, workflow, req); handled {
				return err
			}
		}
		stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, usedFallback, err := s.streamChatCompletion(ctx, workflow, streamReq, providerType, providerName, usageModel)
		if err != nil {
			return handleError(c, err)
		}
		if usedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(c, workflow, resolvedUsageModel, resolvedProviderType, resolvedProviderName, failoverModel, stream)
	}

	resp, providerType, providerName, failoverModel, usedFallback, err := s.executeChatCompletion(ctx, workflow, req)
	if err != nil {
		return handleError(c, err)
	}
	if usedFallback {
		markRequestFallbackUsed(c)
		auditlog.EnrichEntryWithFailover(c, failoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(workflow, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, workflow, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromChatResponse(resp, requestID, providerType, "/v1/chat/completions", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) Responses(c *echo.Context) error {
	return s.responsesHandler(c)
}

// handleTranslatedInference is the shared decode→workflow→patch→dispatch pipeline
// for ChatCompletion and Responses, parameterised over the request type.
func handleTranslatedInference[R any](
	s *translatedInferenceService,
	c *echo.Context,
	decode func([]byte, *core.WhiteBoxPrompt) (R, error),
	modelProvider func(R) (*string, *string),
	patch func(context.Context, R) (R, error),
	isStream func(R) bool,
	dispatch func(*echo.Context, R, *core.Workflow) error,
) error {
	req, err := canonicalJSONRequestFromSemantics(c, decode)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	modelPtr, providerPtr := modelProvider(req)
	workflow, err := ensureTranslatedRequestWorkflowWithAuthorizer(c, s.provider, s.modelResolver, s.modelAuthorizer, s.workflowPolicyResolver, modelPtr, providerPtr)
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

	return handleWithCache(s, c, req, isStream(req), workflow, dispatch)
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
	workflow *core.Workflow,
	dispatch func(*echo.Context, R, *core.Workflow) error,
) error {
	ctx := s.withCacheRequestContext(c.Request().Context(), workflow)
	c.SetRequest(c.Request().WithContext(ctx))

	if s.responseCache != nil && (workflow == nil || workflow.CacheEnabled()) {
		body, marshalErr := marshalRequestBody(req)
		if marshalErr != nil {
			slog.Debug("marshalRequestBody failed", "err", marshalErr)
		} else {
			return s.responseCache.HandleRequest(c, body, func() error {
				return dispatch(c, req, workflow)
			})
		}
	}

	return dispatch(c, req, workflow)
}

func (s *translatedInferenceService) withCacheRequestContext(ctx context.Context, workflow *core.Workflow) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if workflow != nil {
		ctx = core.WithWorkflow(ctx, workflow)
	}
	if workflow != nil && workflow.Policy != nil {
		return core.WithGuardrailsHash(ctx, workflow.GuardrailsHash())
	}
	if s.guardrailsHash != "" {
		return core.WithGuardrailsHash(ctx, s.guardrailsHash)
	}
	return ctx
}

func (s *translatedInferenceService) dispatchResponses(c *echo.Context, req *core.ResponsesRequest, workflow *core.Workflow) error {
	ctx := c.Request().Context()
	_, providerType, providerName, usageModel := s.resolveProviderAndModelFromWorkflow(c, workflow, req.Model, nil)
	requestID := requestIDFromContextOrHeader(c.Request())

	if req.Stream {
		if (workflow == nil || workflow.UsageEnabled()) && s.shouldEnforceReturningUsageData() {
			ctx = core.WithEnforceReturningUsageData(ctx, true)
		}
		stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, usedFallback, err := s.streamResponses(ctx, workflow, req, providerType, providerName, usageModel)
		if err != nil {
			return handleError(c, err)
		}
		if usedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(c, workflow, resolvedUsageModel, resolvedProviderType, resolvedProviderName, failoverModel, stream)
	}

	resp, providerType, providerName, failoverModel, usedFallback, err := s.executeResponses(ctx, workflow, req)
	if err != nil {
		return handleError(c, err)
	}
	if usedFallback {
		markRequestFallbackUsed(c)
		auditlog.EnrichEntryWithFailover(c, failoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(workflow, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, workflow, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromResponsesResponse(resp, requestID, providerType, "/v1/responses", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) tryFastPathStreamingChatPassthrough(c *echo.Context, workflow *core.Workflow, req *core.ChatRequest) (bool, error) {
	if !s.canFastPathStreamingChatPassthrough(workflow, req) {
		return false, nil
	}

	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return false, nil
	}

	ctx, _ := requestContextWithRequestID(c.Request())
	c.SetRequest(c.Request().WithContext(ctx))

	const endpoint = "/chat/completions"
	providerType := strings.TrimSpace(workflow.ProviderType)
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
		Model:       resolvedModelFromWorkflow(workflow, req.Model),
	}
	passthrough := passthroughService{
		provider:        s.provider,
		logger:          s.logger,
		usageLogger:     s.usageLogger,
		pricingResolver: s.pricingResolver,
	}
	return true, passthrough.proxyPassthroughResponse(c, providerType, providerNameFromWorkflow(workflow), endpoint, info, resp)
}

func (s *translatedInferenceService) canFastPathStreamingChatPassthrough(workflow *core.Workflow, req *core.ChatRequest) bool {
	if req == nil || !req.Stream {
		return false
	}
	if s.translatedRequestPatcher != nil || s.shouldEnforceReturningUsageData() {
		return false
	}
	if workflow == nil || workflow.Resolution == nil {
		return false
	}

	providerType := strings.ToLower(strings.TrimSpace(workflow.ProviderType))
	switch providerType {
	case "openai", "azure", "openrouter":
	default:
		return false
	}

	if translatedStreamingSelectorRewriteRequired(workflow.Resolution) {
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
	workflow, err := ensureTranslatedRequestWorkflowWithAuthorizer(c, s.provider, s.modelResolver, s.modelAuthorizer, s.workflowPolicyResolver, &req.Model, &req.Provider)
	if err != nil {
		return handleError(c, err)
	}

	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	resp, providerType, providerName, err := s.executeEmbeddings(ctx, workflow, req)
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(workflow, resp.Model, providerName), providerType, providerName)

	s.logUsage(ctx, workflow, resp.Model, providerType, providerName, func(pricing *core.ModelPricing) *usage.UsageEntry {
		return usage.ExtractFromEmbeddingResponse(resp, requestID, providerType, "/v1/embeddings", pricing)
	})

	return c.JSON(http.StatusOK, resp)
}

func (s *translatedInferenceService) handleStreamingReadCloser(
	c *echo.Context,
	workflow *core.Workflow,
	model, provider, providerName string,
	failoverModel string,
	stream io.ReadCloser,
) error {
	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)
	auditlog.EnrichEntryWithFailover(c, failoverModel)
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(workflow, model, providerName), provider, providerName)

	entry := auditlog.GetStreamEntryFromContext(c)
	auditEnabled := s.logger != nil && s.logger.Config().Enabled && (workflow == nil || workflow.AuditEnabled())
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
	if s.usageLogger != nil && s.usageLogger.Config().Enabled && (workflow == nil || workflow.UsageEnabled()) {
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
	workflow *core.Workflow,
	model, provider, providerName string,
	streamFn func() (io.ReadCloser, error),
) error {
	stream, err := streamFn()
	if err != nil {
		return handleError(c, err)
	}
	return s.handleStreamingReadCloser(c, workflow, model, provider, providerName, "", stream)
}

//nolint:dupl // typed wrapper over the shared translated fallback executor
func (s *translatedInferenceService) executeChatCompletion(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ChatRequest,
) (*core.ChatResponse, string, string, string, bool, error) {
	return executeTranslatedWithFallback(ctx, s, workflow, req, req.Model, req.Provider, cloneChatRequestForSelector,
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
	workflow *core.Workflow,
	req *core.ChatRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, string, bool, error) {
	stream, err := s.provider.StreamChatCompletion(ctx, req)
	if err == nil {
		return stream, providerType, providerName, usageModel, "", false, nil
	}

	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, err := tryFallbackStream(ctx, s, workflow, req.Model, req.Provider, err,
		func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error) {
			stream, err := s.provider.StreamChatCompletion(ctx, cloneChatRequestForSelector(req, selector))
			if err != nil {
				return nil, "", "", err
			}
			return stream, providerType, selector.Model, nil
		},
	)
	if err != nil {
		return nil, "", "", "", "", false, err
	}
	return stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, true, nil
}

//nolint:dupl // typed wrapper over the shared translated fallback executor
func (s *translatedInferenceService) executeResponses(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.ResponsesRequest,
) (*core.ResponsesResponse, string, string, string, bool, error) {
	return executeTranslatedWithFallback(ctx, s, workflow, req, req.Model, req.Provider, cloneResponsesRequestForSelector,
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
	workflow *core.Workflow,
	req *core.ResponsesRequest,
	providerType, providerName, usageModel string,
) (io.ReadCloser, string, string, string, string, bool, error) {
	stream, err := s.provider.StreamResponses(ctx, req)
	if err == nil {
		return stream, providerType, providerName, usageModel, "", false, nil
	}

	stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, err := tryFallbackStream(ctx, s, workflow, req.Model, req.Provider, err,
		func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error) {
			stream, err := s.provider.StreamResponses(ctx, cloneResponsesRequestForSelector(req, selector))
			if err != nil {
				return nil, "", "", err
			}
			return stream, providerType, selector.Model, nil
		},
	)
	if err != nil {
		return nil, "", "", "", "", false, err
	}
	return stream, resolvedProviderType, resolvedProviderName, resolvedUsageModel, failoverModel, true, nil
}

func (s *translatedInferenceService) executeEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
) (*core.EmbeddingResponse, string, string, error) {
	providerType := providerTypeFromWorkflow(workflow)
	providerName := providerNameFromWorkflow(workflow)
	resp, err := s.provider.Embeddings(ctx, req)
	if err == nil {
		return resp, responseProviderType(providerType, resp.Provider), providerName, nil
	}

	return s.tryFallbackEmbeddings(ctx, workflow, req, err)
}

func (s *translatedInferenceService) tryFallbackEmbeddings(
	ctx context.Context,
	workflow *core.Workflow,
	req *core.EmbeddingRequest,
	primaryErr error,
) (*core.EmbeddingResponse, string, string, error) {
	// Embeddings fallback is intentionally disabled until the shared model
	// contract can prove vector-size compatibility for alternates.
	return nil, "", "", primaryErr
}

func (s *translatedInferenceService) logUsage(
	ctx context.Context,
	workflow *core.Workflow,
	model, providerType, providerName string,
	extractFn func(*core.ModelPricing) *usage.UsageEntry,
) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled || (workflow != nil && !workflow.UsageEnabled()) {
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

func (s *translatedInferenceService) fallbackSelectors(workflow *core.Workflow) []core.ModelSelector {
	if s.fallbackResolver == nil || workflow == nil || workflow.Resolution == nil || !workflow.FallbackEnabled() {
		return nil
	}
	return s.fallbackResolver.ResolveFallbacks(workflow.Resolution, workflow.Endpoint.Operation)
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

func (s *translatedInferenceService) resolveProviderAndModelFromWorkflow(
	c *echo.Context,
	workflow *core.Workflow,
	fallbackModel string,
	req *core.ChatRequest,
) (*core.ChatRequest, string, string, string) {
	providerType := GetProviderType(c)
	providerName := providerNameFromWorkflow(workflow)
	if workflow != nil {
		if workflowProviderType := strings.TrimSpace(workflow.ProviderType); workflowProviderType != "" {
			providerType = workflowProviderType
		}
	}

	model := resolvedModelFromWorkflow(workflow, fallbackModel)
	if req == nil || !req.Stream || (workflow != nil && !workflow.UsageEnabled()) || !s.shouldEnforceReturningUsageData() {
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

func providerNameFromWorkflow(workflow *core.Workflow) string {
	if workflow == nil || workflow.Resolution == nil {
		return ""
	}
	return strings.TrimSpace(workflow.Resolution.ProviderName)
}

func resolvedModelPrefix(workflow *core.Workflow, providerName string) string {
	if providerName = strings.TrimSpace(providerName); providerName != "" {
		return providerName
	}
	if workflow == nil || workflow.Resolution == nil {
		return ""
	}
	if providerName = strings.TrimSpace(workflow.Resolution.ProviderName); providerName != "" {
		return providerName
	}
	return strings.TrimSpace(workflow.Resolution.ResolvedSelector.Provider)
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

func qualifyExecutedModel(workflow *core.Workflow, model, providerName string) string {
	return qualifyModelWithProvider(model, resolvedModelPrefix(workflow, providerName))
}

func markRequestFallbackUsed(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	c.SetRequest(c.Request().WithContext(core.WithFallbackUsed(c.Request().Context())))
}

func resolvedModelFromWorkflow(workflow *core.Workflow, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if workflow == nil || workflow.Resolution == nil {
		return fallback
	}
	if resolvedModel := strings.TrimSpace(workflow.Resolution.ResolvedSelector.Model); resolvedModel != "" {
		return resolvedModel
	}
	return fallback
}

// marshalRequestBody serializes a patched request struct to JSON bytes for cache key computation.
// Returns an error only on marshalling failure; callers bypass cache on error.
func marshalRequestBody(req any) ([]byte, error) {
	return json.Marshal(req)
}

func providerTypeFromWorkflow(workflow *core.Workflow) string {
	if workflow == nil {
		return ""
	}
	return strings.TrimSpace(workflow.ProviderType)
}

func currentSelectorForWorkflow(workflow *core.Workflow, model, provider string) string {
	if workflow != nil && workflow.Resolution != nil {
		if resolved := strings.TrimSpace(workflow.Resolution.ResolvedQualifiedModel()); resolved != "" {
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
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	var zero T

	fallbacks := s.fallbackSelectors(workflow)
	if len(fallbacks) == 0 || !shouldAttemptFallback(primaryErr) {
		return zero, "", "", "", false, primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		if s.modelAuthorizer != nil && !s.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := s.providerTypeForSelector(selector, providerTypeFromWorkflow(workflow))
		providerName := resolvedProviderName(s.provider, selector, providerNameFromWorkflow(workflow))
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
	s *translatedInferenceService,
	workflow *core.Workflow,
	model, provider string,
	primary func() (T, string, string, error),
	fallback func(selector core.ModelSelector, providerType, providerName string) (T, string, error),
) (T, string, string, string, bool, error) {
	resp, resolvedProviderType, resolvedProviderName, err := primary()
	if err == nil {
		return resp, resolvedProviderType, resolvedProviderName, "", false, nil
	}
	return tryFallbackResponse(ctx, s, workflow, model, provider, err, fallback)
}

func executeTranslatedWithFallback[Req any, Resp any](
	ctx context.Context,
	s *translatedInferenceService,
	workflow *core.Workflow,
	req Req,
	model, provider string,
	cloneForSelector func(Req, core.ModelSelector) Req,
	call func(context.Context, Req) (Resp, string, error),
) (Resp, string, string, string, bool, error) {
	return executeWithFallbackResponse(ctx, s, workflow, model, provider,
		func() (Resp, string, string, error) {
			resp, responseProvider, err := call(ctx, req)
			if err != nil {
				var zero Resp
				return zero, "", "", err
			}
			return resp, responseProviderType(providerTypeFromWorkflow(workflow), responseProvider), providerNameFromWorkflow(workflow), nil
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
	workflow *core.Workflow,
	model, provider string,
	primaryErr error,
	call func(selector core.ModelSelector, providerType, providerName string) (io.ReadCloser, string, string, error),
) (io.ReadCloser, string, string, string, string, error) {
	fallbacks := s.fallbackSelectors(workflow)
	if len(fallbacks) == 0 || !shouldAttemptFallback(primaryErr) {
		return nil, "", "", "", "", primaryErr
	}

	requestID := strings.TrimSpace(core.GetRequestID(ctx))
	primaryModel := currentSelectorForWorkflow(workflow, model, provider)
	lastErr := primaryErr
	for _, selector := range fallbacks {
		if s.modelAuthorizer != nil && !s.modelAuthorizer.AllowsModel(ctx, selector) {
			continue
		}
		qualified := selector.QualifiedModel()
		providerType := s.providerTypeForSelector(selector, providerTypeFromWorkflow(workflow))
		providerName := resolvedProviderName(s.provider, selector, providerNameFromWorkflow(workflow))
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
