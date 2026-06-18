package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"syscall"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/conversationstore"
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
	conversationStore        conversationstore.Store
	conversationStoreMu      sync.RWMutex

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
			return handleStreamingDispatchError(c, err)
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
			nil,
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
	ctx, preparedReq, workflow, err := unpackPrepared(ctx, prepared, err, responsesPreparedFields)
	if err != nil {
		return ctx, preparedReq, workflow, err
	}
	// Resolve gateway-managed conversations before caching and dispatch so the
	// cache key reflects the merged history and providers never see local IDs.
	ctx, preparedReq, err = s.applyResponsesConversation(ctx, preparedReq)
	return ctx, preparedReq, workflow, err
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
// Cache hits intentionally return before dispatch and budget enforcement because
// they do not incur provider spend. Cache misses still run dispatch, where
// dispatchChatCompletion and dispatchResponses call enforceBudget before any
// provider request.
func handleWithCache[R any](
	s *translatedInferenceService,
	c *echo.Context,
	req R,
	workflow *core.Workflow,
	dispatch func(*echo.Context, R, *core.Workflow) error,
) error {
	// Conversation turns are stateful: the same input means something different
	// as the conversation grows, and a cache hit would skip the history append.
	if conversationTurnFromContext(c.Request().Context()) != nil {
		return dispatch(c, req, workflow)
	}

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
			return handleStreamingDispatchError(c, err)
		}
		if result.Meta.UsedFallback {
			markRequestFallbackUsed(c)
		}
		stream := result.Stream
		if turn := conversationTurnFromContext(ctx); turn != nil {
			stream = streaming.NewObservedSSEStream(stream, turn.streamObserver(ctx))
		}
		return s.handleStreamingReadCloser(
			c,
			workflow,
			result.Meta.Model,
			result.Meta.ProviderType,
			result.Meta.ProviderName,
			result.Meta.FailoverModel,
			stream,
			nil,
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
	if turn := conversationTurnFromContext(ctx); turn != nil {
		// Detach cancellation so a client disconnect after provider success
		// cannot lose the completed turn, mirroring the streaming observer.
		turn.appendResponse(context.WithoutCancel(ctx), result.Response)
	}

	return c.JSON(http.StatusOK, result.Response)
}

func (s *translatedInferenceService) storeResponseSnapshot(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, resp *core.ResponsesResponse, providerType, providerName, requestID string) error {
	store := s.currentResponseStore()
	if store == nil || resp == nil || resp.ID == "" {
		return nil
	}
	if req != nil && req.Store != nil && !*req.Store {
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

func (s *translatedInferenceService) currentConversationStore() conversationstore.Store {
	s.conversationStoreMu.RLock()
	defer s.conversationStoreMu.RUnlock()
	return s.conversationStore
}

func (s *translatedInferenceService) setConversationStore(store conversationstore.Store) {
	s.conversationStoreMu.Lock()
	defer s.conversationStoreMu.Unlock()
	s.conversationStore = store
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

// handleStreamingReadCloser flushes a provider SSE stream to the client while
// fanning audit and usage observers off the canonical (OpenAI-shaped) stream.
// outerWrap, when non-nil, wraps the observed stream as the outermost layer —
// used by the Anthropic /v1/messages dialect to re-encode the SSE events after
// the observers have already seen the canonical form.
func (s *translatedInferenceService) handleStreamingReadCloser(
	c *echo.Context,
	workflow *core.Workflow,
	model, provider, providerName string,
	failoverModel string,
	stream io.ReadCloser,
	outerWrap func(io.ReadCloser) io.ReadCloser,
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
	if outerWrap != nil {
		wrappedStream = outerWrap(wrappedStream)
	}

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
		recordStreamingError(streamEntry, model, provider, c.Request().URL.Path, requestID, c.Request().Context(), err)
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
		return handleStreamingDispatchError(c, err)
	}
	return s.handleStreamingReadCloser(c, workflow, model, provider, providerName, "", stream, nil)
}

// handleStreamingDispatchError records audit context for a streaming request
// that failed before any chunks could be flushed. It marks the entry as
// streaming and distinguishes client cancellations from upstream failures so
// the audit log reflects the actual cause.
func handleStreamingDispatchError(c *echo.Context, err error) error {
	auditlog.EnrichEntryWithStream(c, true)
	if isClientDisconnectDuringDispatch(c.Request().Context(), err) {
		auditlog.EnrichEntryWithError(c, "client_disconnected", err.Error(), "")
		return nil
	}
	return handleError(c, err)
}

func recordStreamingError(streamEntry *auditlog.LogEntry, model, provider, path, requestID string, ctx context.Context, err error) {
	errorType := "stream_error"
	if isClientDisconnect(ctx, err) {
		errorType = "client_disconnected"
	}

	// The nil-err branch in isClientDisconnect is reachable for callers that
	// only have a canceled context to report. Fall back to the context error
	// in that case so we never dereference a nil error.
	logErr := err
	errorMessage := ""
	switch {
	case err != nil:
		errorMessage = err.Error()
	case ctx != nil && ctx.Err() != nil:
		logErr = ctx.Err()
		errorMessage = logErr.Error()
	}

	if streamEntry != nil {
		streamEntry.ErrorType = errorType
		if streamEntry.Data == nil {
			streamEntry.Data = &auditlog.LogData{}
		}
		streamEntry.Data.ErrorMessage = errorMessage
	}

	slog.Warn("stream terminated abnormally",
		"error", logErr,
		"error_type", errorType,
		"model", model,
		"provider", provider,
		"path", path,
		"request_id", requestID,
	)
}

// isClientDisconnect classifies write-phase streaming errors (errors returned
// after the gateway has begun writing the SSE response back to the client). At
// this phase EPIPE / ECONNRESET on the response writer can only come from the
// downstream client connection, so they are treated as client disconnects. The
// nil-err / canceled-context branch supports callers that only have a context
// signal to report.
func isClientDisconnect(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	return err == nil && ctx != nil && ctx.Err() == context.Canceled
}

// isClientDisconnectDuringDispatch classifies a streaming dispatch error - one
// that happened before any response bytes were flushed to the client. At this
// phase the only socket in play is the upstream provider connection, so
// EPIPE / ECONNRESET on err belong to the provider and must NOT be swallowed
// as client disconnects. Only a cancellation of the request context proves
// the client is gone. The ctx-only branch still requires err == nil so a
// concrete upstream failure racing with a cancellation surfaces as a real
// upstream error.
func isClientDisconnectDuringDispatch(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	return err == nil && ctx != nil && ctx.Err() == context.Canceled
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
