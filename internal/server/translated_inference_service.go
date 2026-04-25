package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
	"gomodel/internal/gateway"
	"gomodel/internal/observability"
	"gomodel/internal/responsecache"
	"gomodel/internal/responsestore"
	"gomodel/internal/streaming"
	"gomodel/internal/usage"
)

// translatedInferenceService adapts Echo requests to the transport-independent
// translated inference orchestrator.
type translatedInferenceService struct {
	provider                 core.RoutableProvider
	modelResolver            RequestModelResolver
	modelAuthorizer          RequestModelAuthorizer
	workflowPolicyResolver   RequestWorkflowPolicyResolver
	fallbackResolver         RequestFallbackResolver
	translatedRequestPatcher TranslatedRequestPatcher
	logger                   auditlog.LoggerInterface
	usageLogger              usage.LoggerInterface
	budgetChecker            BudgetChecker
	pricingResolver          usage.PricingResolver
	responseCache            *responsecache.ResponseCacheMiddleware
	guardrailsHash           string
	responseStore            responsestore.Store
	responseStoreMu          sync.RWMutex

	orchestrator *gateway.InferenceOrchestrator

	chatCompletionHandler echo.HandlerFunc
	responsesHandler      echo.HandlerFunc
}

func (s *translatedInferenceService) initHandlers() {
	s.orchestrator = s.newInferenceOrchestrator()
	s.chatCompletionHandler = s.handleChatCompletion
	s.responsesHandler = s.handleResponses
}

func (s *translatedInferenceService) inference() *gateway.InferenceOrchestrator {
	return s.orchestrator
}

func (s *translatedInferenceService) newInferenceOrchestrator() *gateway.InferenceOrchestrator {
	return gateway.NewInferenceOrchestrator(gateway.InferenceConfig{
		Provider:                 s.provider,
		ModelResolver:            s.modelResolver,
		ModelAuthorizer:          s.modelAuthorizer,
		WorkflowPolicyResolver:   s.workflowPolicyResolver,
		FallbackResolver:         s.fallbackResolver,
		TranslatedRequestPatcher: s.translatedRequestPatcher,
		UsageLogger:              s.usageLogger,
		PricingResolver:          s.pricingResolver,
		GuardrailsHash:           s.guardrailsHash,
	})
}

func (s *translatedInferenceService) ChatCompletion(c *echo.Context) error {
	return s.chatCompletionHandler(c)
}

func (s *translatedInferenceService) handleChatCompletion(c *echo.Context) error {
	return handleTranslatedJSON(s, c, core.DecodeChatRequest, prepareChatCompletionRequest, s.dispatchChatCompletion)
}

func (s *translatedInferenceService) dispatchChatCompletion(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow) error {
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return handleError(c, err)
	}

	if req.Stream {
		if len(s.inference().FallbackSelectors(workflow)) == 0 {
			if handled, err := s.tryFastPathStreamingChatPassthrough(c, workflow, req); handled {
				return err
			}
		}
		result, err := s.inference().StreamChatCompletion(ctx, workflow, req)
		if err != nil {
			return handleError(c, err)
		}
		if result.Meta.UsedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(
			c,
			workflow,
			result.Meta.Model,
			result.Meta.ProviderType,
			result.Meta.ProviderName,
			result.Meta.FailoverModel,
			result.Stream,
		)
	}

	result, err := s.inference().ExecuteChatCompletion(ctx, workflow, req, requestID, "/v1/chat/completions")
	if err != nil {
		return handleError(c, err)
	}
	if result.Meta.UsedFallback {
		markRequestFallbackUsed(c)
		auditlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	return c.JSON(http.StatusOK, result.Response)
}

func (s *translatedInferenceService) Responses(c *echo.Context) error {
	return s.responsesHandler(c)
}

func (s *translatedInferenceService) handleResponses(c *echo.Context) error {
	return handleTranslatedJSON(s, c, core.DecodeResponsesRequest, prepareResponsesRequest, s.dispatchResponses)
}

func handleTranslatedJSON[Req any](
	s *translatedInferenceService,
	c *echo.Context,
	decode func([]byte, *core.WhiteBoxPrompt) (Req, error),
	prepare func(*translatedInferenceService, context.Context, Req, gateway.RequestMeta) (context.Context, Req, *core.Workflow, error),
	dispatch func(*echo.Context, Req, *core.Workflow) error,
) error {
	req, err := canonicalJSONRequestFromSemantics[Req](c, decode)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	ctx, preparedReq, workflow, err := prepare(s, c.Request().Context(), req, translatedRequestMeta(c))
	if err != nil {
		return handleError(c, err)
	}
	attachPreparedWorkflow(c, ctx, workflow)

	return handleWithCache(s, c, preparedReq, workflow, dispatch)
}

func prepareChatCompletionRequest(
	s *translatedInferenceService,
	ctx context.Context,
	req *core.ChatRequest,
	meta gateway.RequestMeta,
) (context.Context, *core.ChatRequest, *core.Workflow, error) {
	prepared, err := s.inference().PrepareChatRequest(ctx, req, meta)
	return unpackPrepared(ctx, prepared, err, chatPreparedFields)
}

func prepareResponsesRequest(
	s *translatedInferenceService,
	ctx context.Context,
	req *core.ResponsesRequest,
	meta gateway.RequestMeta,
) (context.Context, *core.ResponsesRequest, *core.Workflow, error) {
	prepared, err := s.inference().PrepareResponsesRequest(ctx, req, meta)
	return unpackPrepared(ctx, prepared, err, responsesPreparedFields)
}

func unpackPrepared[Prepared any, Req any](
	fallback context.Context,
	prepared Prepared,
	err error,
	fields func(Prepared) (context.Context, Req, *core.Workflow),
) (context.Context, Req, *core.Workflow, error) {
	if err != nil {
		var zero Req
		return fallback, zero, nil, err
	}
	ctx, req, workflow := fields(prepared)
	return ctx, req, workflow, nil
}

func chatPreparedFields(prepared *gateway.PreparedChatRequest) (context.Context, *core.ChatRequest, *core.Workflow) {
	return prepared.Context, prepared.Request, prepared.Workflow
}

func responsesPreparedFields(prepared *gateway.PreparedResponsesRequest) (context.Context, *core.ResponsesRequest, *core.Workflow) {
	return prepared.Context, prepared.Request, prepared.Workflow
}

// handleWithCache routes translated requests through the response cache when
// enabled. The request has already been resolved and patched by the orchestrator.
func handleWithCache[R any](
	s *translatedInferenceService,
	c *echo.Context,
	req R,
	workflow *core.Workflow,
	dispatch func(*echo.Context, R, *core.Workflow) error,
) error {
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

func (s *translatedInferenceService) dispatchResponses(c *echo.Context, req *core.ResponsesRequest, workflow *core.Workflow) error {
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return handleError(c, err)
	}

	if req.Stream {
		result, err := s.inference().StreamResponses(ctx, workflow, req)
		if err != nil {
			return handleError(c, err)
		}
		if result.Meta.UsedFallback {
			markRequestFallbackUsed(c)
		}
		return s.handleStreamingReadCloser(
			c,
			workflow,
			result.Meta.Model,
			result.Meta.ProviderType,
			result.Meta.ProviderName,
			result.Meta.FailoverModel,
			result.Stream,
		)
	}

	result, err := s.inference().ExecuteResponses(ctx, workflow, req, requestID, "/v1/responses")
	if err != nil {
		return handleError(c, err)
	}
	if result.Meta.UsedFallback {
		markRequestFallbackUsed(c)
		auditlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	if err := s.storeResponseSnapshot(ctx, workflow, req, result.Response, result.Meta.ProviderType, result.Meta.ProviderName, requestID); err != nil {
		s.recordResponseSnapshotStoreFailure(workflow, result.Response, result.Meta.ProviderType, result.Meta.ProviderName, requestID, err)
	}

	return c.JSON(http.StatusOK, result.Response)
}

func (s *translatedInferenceService) storeResponseSnapshot(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, resp *core.ResponsesResponse, providerType, providerName, requestID string) error {
	store := s.currentResponseStore()
	if store == nil || resp == nil || resp.ID == "" {
		return nil
	}

	stored := &responsestore.StoredResponse{
		Response:           resp,
		InputItems:         normalizedResponseInputItems(resp.ID, req),
		Provider:           strings.TrimSpace(providerType),
		ProviderName:       strings.TrimSpace(providerName),
		ProviderResponseID: resp.ID,
		RequestID:          requestID,
		UserPath:           core.UserPathFromContext(ctx),
		WorkflowVersionID:  workflow.WorkflowVersionID(),
	}
	if createErr := store.Create(ctx, stored); createErr != nil {
		updateErr := store.Update(ctx, stored)
		if updateErr == nil {
			return nil
		}
		return core.NewProviderError("response_store", http.StatusInternalServerError, "failed to persist response", errors.Join(createErr, updateErr))
	}
	return nil
}

func (s *translatedInferenceService) currentResponseStore() responsestore.Store {
	s.responseStoreMu.RLock()
	defer s.responseStoreMu.RUnlock()
	return s.responseStore
}

func (s *translatedInferenceService) setResponseStore(store responsestore.Store) {
	s.responseStoreMu.Lock()
	defer s.responseStoreMu.Unlock()
	s.responseStore = store
}

func (s *translatedInferenceService) recordResponseSnapshotStoreFailure(workflow *core.Workflow, resp *core.ResponsesResponse, providerType, providerName, requestID string, err error) {
	observability.ResponseSnapshotStoreFailures.WithLabelValues(
		strings.TrimSpace(providerType),
		strings.TrimSpace(providerName),
		"store",
	).Inc()

	slog.Warn("response snapshot store failed",
		"request_id", requestID,
		"provider_type", providerType,
		"provider_name", providerName,
		"workflow_version_id", workflow.WorkflowVersionID(),
		"response_id", responseIDForLog(resp),
		"error", err,
	)
}

func responseIDForLog(resp *core.ResponsesResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.ID)
}

func (s *translatedInferenceService) tryFastPathStreamingChatPassthrough(c *echo.Context, workflow *core.Workflow, req *core.ChatRequest) (bool, error) {
	if !s.inference().CanFastPathStreamingChatPassthrough(workflow, req) {
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

func (s *translatedInferenceService) Embeddings(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.EmbeddingRequest](c, core.DecodeEmbeddingRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	prepared, err := s.inference().PrepareEmbeddingRequest(c.Request().Context(), req, translatedRequestMeta(c))
	if err != nil {
		return handleError(c, err)
	}
	attachPreparedWorkflow(c, prepared.Context, prepared.Workflow)

	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return handleError(c, err)
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	result, err := s.inference().ExecuteEmbeddings(c.Request().Context(), prepared.Workflow, prepared.Request, requestID, "/v1/embeddings")
	if err != nil {
		return handleError(c, err)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(prepared.Workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	return c.JSON(http.StatusOK, result.Response)
}

func translatedRequestMeta(c *echo.Context) gateway.RequestMeta {
	return gateway.RequestMeta{
		RequestID: requestIDFromContextOrHeader(c.Request()),
		Endpoint:  core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path),
		Workflow:  core.GetWorkflow(c.Request().Context()),
	}
}

func attachPreparedWorkflow(c *echo.Context, ctx context.Context, workflow *core.Workflow) {
	if ctx != nil {
		c.SetRequest(c.Request().WithContext(ctx))
	}
	cacheWorkflowResolutionHints(c, workflow)
	storeWorkflow(c, workflow)
}

func cacheWorkflowResolutionHints(c *echo.Context, workflow *core.Workflow) {
	if c == nil || workflow == nil || workflow.Resolution == nil {
		return
	}
	if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil {
		env.RouteHints.Model = workflow.Resolution.ResolvedSelector.Model
		env.RouteHints.Provider = workflow.Resolution.ResolvedSelector.Provider
	}
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

func providerNameFromWorkflow(workflow *core.Workflow) string {
	return gateway.ProviderNameFromWorkflow(workflow)
}

func qualifyExecutedModel(workflow *core.Workflow, model, providerName string) string {
	return gateway.QualifyExecutedModel(workflow, model, providerName)
}

func markRequestFallbackUsed(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	c.SetRequest(c.Request().WithContext(core.WithFallbackUsed(c.Request().Context())))
}

func resolvedModelFromWorkflow(workflow *core.Workflow, fallback string) string {
	return gateway.ResolvedModelFromWorkflow(workflow, fallback)
}

func marshalRequestBody(req any) ([]byte, error) {
	return json.Marshal(req)
}
