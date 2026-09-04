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
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/observability"
	"github.com/enterpilot/gomodel/internal/plugins"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/pluginapi"
)

// translatedInferenceService adapts Echo requests to the transport-independent
// translated inference orchestrator.
type translatedInferenceService struct {
	provider                 core.RoutableProvider
	modelResolver            RequestModelResolver
	modelAuthorizer          RequestModelAuthorizer
	workflowPolicyResolver   RequestWorkflowPolicyResolver
	failoverResolver         RequestFailoverResolver
	failoverPolicy           *gateway.FailoverPolicy
	translatedRequestPatcher TranslatedRequestPatcher
	pluginChains             PluginChainsResolver
	logger                   auditlog.LoggerInterface
	usageLogger              usage.LoggerInterface
	budgetChecker            BudgetChecker
	rateLimiter              RateLimiter
	pricingResolver          usage.PricingResolver
	responseCache            *responsecache.ResponseCacheMiddleware
	guardrailsHash           string
	responseStore            responsestore.Store
	responseStoreMu          sync.RWMutex
	conversationStore        conversationstore.Store
	conversationStoreMu      sync.RWMutex
	// snapshotWrites tracks background response snapshot writes so shutdown
	// can drain them before closing the response store. snapshotMu gates new
	// writes against the drain: a handler that outlives the HTTP drain window
	// must not register a write after drainSnapshotWrites has begun waiting.
	snapshotWrites   sync.WaitGroup
	snapshotMu       sync.RWMutex
	snapshotDraining bool

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
	cfg := gateway.InferenceConfig{
		Provider:                 s.provider,
		ModelResolver:            s.modelResolver,
		ModelAuthorizer:          s.modelAuthorizer,
		WorkflowPolicyResolver:   s.workflowPolicyResolver,
		FailoverResolver:         s.failoverResolver,
		FailoverPolicy:           s.failoverPolicy,
		TranslatedRequestPatcher: s.translatedRequestPatcher,
		UsageLogger:              s.usageLogger,
		PricingResolver:          s.pricingResolver,
		GuardrailsHash:           s.guardrailsHash,
	}
	// Guarded assignment keeps the gate nil when rate limits are off (a nil
	// RateLimiter assigned unconditionally would arrive as a typed non-nil
	// RouteGate).
	if s.rateLimiter != nil {
		cfg.RouteGate = s.rateLimiter
	}
	return gateway.NewInferenceOrchestrator(cfg)
}

func (s *translatedInferenceService) ChatCompletion(c *echo.Context) error {
	return s.chatCompletionHandler(c)
}

func (s *translatedInferenceService) handleChatCompletion(c *echo.Context) error {
	return handleTranslatedJSON(s, c, core.DecodeChatRequest, prepareChatCompletionRequest, s.dispatchChatCompletion)
}

func (s *translatedInferenceService) dispatchChatCompletion(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow) error {
	s.observeLiveProviderAttempts(c, workflow)
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker,
		rateLimitRouteFromWorkflow(workflow).withFailovers(len(s.inference().FailoverSelectors(workflow))))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx = adm.dispatchContext(ctx)

	feedbackEnabled := hasResponseFeedbackObservers(c)

	if req.Stream {
		if feedbackEnabled {
			req = gateway.CloneChatRequestForStreamUsage(req)
			if req.StreamOptions == nil {
				req.StreamOptions = &core.StreamOptions{}
			}
			req.StreamOptions.IncludeUsage = true
		}
		result, err := s.inference().StreamChatCompletion(ctx, workflow, req)
		if err != nil {
			return handleStreamingDispatchError(c, err)
		}
		if result.Meta.UsedFailover {
			markRequestFailoverUsed(c)
		}
		stream := s.wrapPluginStream(ctx, workflow, chatStreamDialect(includeStreamUsage(req)), chatPromptOf(req), result.Stream)
		return s.handleStreamingReadCloser(c, workflow, result.Meta, stream, func(stream io.ReadCloser) io.ReadCloser {
			return result.WrapDeliveryStream(ctx, stream)
		})
	}

	result, err := s.inference().ExecuteChatCompletion(ctx, workflow, req, requestID, "/v1/chat/completions")
	if err != nil {
		return handleError(c, err)
	}
	enrichAuditEntryWithProviderAttempts(c)
	result.Response, err = chatResponsePhase.run(s, c, workflow, req, result.Response)
	if err != nil {
		return handleError(c, err)
	}
	if result.Meta.UsedFailover {
		markRequestFailoverUsed(c)
		auditlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)
	notifyChatResponseFeedback(
		c,
		ext.Endpoint(c.Request().URL.Path),
		result.Response,
		result.Meta.Model,
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	applyPluginResponseHeaders(c)
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

	ctx, preparedReq, workflow, err := prepare(s, withPluginRequestState(c), req, translatedRequestMeta(c))
	if err != nil {
		if short := shortCircuitOf(err); short != nil {
			attachPreparedWorkflow(c, prepareContext(c, ctx), workflow)
			recordPromptPluginRevisions(c, nil, nil)
			return shortCircuit(s, c, workflow, req, short)
		}
		recordPromptPluginRevisions(c, nil, nil)
		return handleError(c, err)
	}
	attachPreparedWorkflow(c, ctx, workflow)
	recordPromptPluginRevisions(c, req, preparedReq)
	applyPluginRequestHeaders(c)

	return handleWithCache(s, c, preparedReq, workflow, dispatch)
}

// shortCircuit renders a prompt-phase respond decision for the request type.
func shortCircuit[Req any](s *translatedInferenceService, c *echo.Context, workflow *core.Workflow, req Req, short *plugins.ShortCircuit) error {
	switch typed := any(req).(type) {
	case *core.ChatRequest:
		return s.writeChatShortCircuit(c, workflow, typed, short, chatJSON, nil)
	case *core.ResponsesRequest:
		return s.writeResponsesShortCircuit(c, workflow, typed, short)
	default:
		return handleError(c, core.NewInvalidRequestError("plugin short-circuit is not supported for this request", nil))
	}
}

func includeStreamUsage(req *core.ChatRequest) bool {
	return req != nil && req.StreamOptions != nil && req.StreamOptions.IncludeUsage
}

// promptOf drops the mapping error: the prompt is a read-only view for
// response-phase plugins and may be absent.
func promptOf(prompt *pluginapi.Prompt, err error) *pluginapi.Prompt {
	if err != nil {
		return nil
	}
	return prompt
}

// chatPromptOf defers the request mapping until a stream wrapper needs it.
func chatPromptOf(req *core.ChatRequest) func() *pluginapi.Prompt {
	return func() *pluginapi.Prompt { return promptOf(exchange.FromChatRequest(req)) }
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
	ctx, req, workflow := fields(prepared)
	if err != nil {
		// A patch-phase error still reports the resolved workflow (and its
		// context) so the caller can render a plugin outcome with it.
		var zero Req
		if ctx == nil {
			ctx = fallback
		}
		return ctx, zero, workflow, err
	}
	return ctx, req, workflow, nil
}

func chatPreparedFields(prepared *gateway.PreparedChatRequest) (context.Context, *core.ChatRequest, *core.Workflow) {
	if prepared == nil {
		return nil, nil, nil
	}
	return prepared.Context, prepared.Request, prepared.Workflow
}

func responsesPreparedFields(prepared *gateway.PreparedResponsesRequest) (context.Context, *core.ResponsesRequest, *core.Workflow) {
	if prepared == nil {
		return nil, nil, nil
	}
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
	s.observeLiveProviderAttempts(c, workflow)
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker,
		rateLimitRouteFromWorkflow(workflow).withFailovers(len(s.inference().FailoverSelectors(workflow))))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()
	ctx = adm.dispatchContext(ctx)

	if req.Stream {
		if hasResponseFeedbackObservers(c) {
			ctx = core.WithEnforceReturningUsageData(ctx, true)
		}
		result, err := s.inference().StreamResponses(ctx, workflow, req)
		if err != nil {
			return handleStreamingDispatchError(c, err)
		}
		if result.Meta.UsedFailover {
			markRequestFailoverUsed(c)
		}
		stream := s.wrapPluginStream(ctx, workflow, responsesStreamDialect(), func() *pluginapi.Prompt { return promptOf(exchange.FromResponsesRequest(req)) }, result.Stream)
		if turn := conversationTurnFromContext(ctx); turn != nil {
			stream = turn.persistingStream(ctx, stream)
		}
		return s.handleStreamingReadCloser(c, workflow, result.Meta, stream, func(stream io.ReadCloser) io.ReadCloser {
			return result.WrapDeliveryStream(ctx, stream)
		})
	}

	result, err := s.inference().ExecuteResponses(ctx, workflow, req, requestID, "/v1/responses")
	if err != nil {
		return handleError(c, err)
	}
	enrichAuditEntryWithProviderAttempts(c)
	result.Response, err = responsesResponsePhase.run(s, c, workflow, req, result.Response)
	if err != nil {
		return handleError(c, err)
	}
	if result.Meta.UsedFailover {
		markRequestFailoverUsed(c)
		auditlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)
	notifyResponsesResponseFeedback(
		c,
		ext.Endpoint(c.Request().URL.Path),
		result.Response,
		result.Meta.Model,
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	if turn := conversationTurnFromContext(ctx); turn != nil {
		// Detach cancellation so a client disconnect after provider success
		// cannot lose the completed turn, mirroring the streaming observer.
		if err := turn.appendResponse(context.WithoutCancel(ctx), result.Response); err != nil {
			return handleError(c, core.NewProviderError(
				"conversation_store", http.StatusInternalServerError, "failed to append conversation turn", err,
			))
		}
	}
	s.storeResponseSnapshotAsync(ctx, workflow, req, result.Response, result.Meta.ProviderType, result.Meta.ProviderName, requestID)

	applyPluginResponseHeaders(c)
	return c.JSON(http.StatusOK, result.Response)
}

// snapshotWriteTimeout bounds one background snapshot write. It covers the
// Create plus the Update fallback, each of which can wait up to SQLite's 5s
// busy timeout on the shared connection.
const snapshotWriteTimeout = 15 * time.Second

// storeResponseSnapshotAsync persists the response snapshot off the request
// path so the client never waits on storage. A failed write is already
// non-fatal on the synchronous path (metric plus warning), so deferring it
// only changes when the failure is observed. The snapshot is detached
// (serialized) before the goroutine starts: the background write must not
// share memory with the response the handler is concurrently serializing to
// the client. The write context is detached from request cancellation, which
// ends the moment the response is sent. drainSnapshotWrites waits for
// in-flight writes at shutdown.
func (s *translatedInferenceService) storeResponseSnapshotAsync(ctx context.Context, workflow *core.Workflow, req *core.ResponsesRequest, resp *core.ResponsesResponse, providerType, providerName, requestID string) {
	store := s.currentResponseStore()
	if store == nil || resp == nil || resp.ID == "" {
		return
	}
	if req != nil && req.Store != nil && !*req.Store {
		return
	}

	failure := snapshotFailureRecord{
		providerType:      providerType,
		providerName:      providerName,
		requestID:         requestID,
		workflowVersionID: workflow.WorkflowVersionID(),
		responseID:        resp.ID,
	}
	snapshot, err := responsestore.Detach(&responsestore.StoredResponse{
		Response:           resp,
		InputItems:         normalizedResponseInputItems(resp.ID, req),
		Provider:           strings.TrimSpace(providerType),
		ProviderName:       strings.TrimSpace(providerName),
		ProviderResponseID: resp.ID,
		RequestID:          requestID,
		UserPath:           core.UserPathFromContext(ctx),
		WorkflowVersionID:  workflow.WorkflowVersionID(),
	})
	if err != nil {
		s.recordResponseSnapshotStoreFailure(failure, err)
		return
	}

	writeCtx := context.WithoutCancel(ctx)
	scheduled := s.goSnapshotWrite(func() {
		writeCtx, cancel := context.WithTimeout(writeCtx, snapshotWriteTimeout)
		defer cancel()
		if err := snapshot.Persist(writeCtx, store); err != nil {
			s.recordResponseSnapshotStoreFailure(failure, err)
		}
	})
	if !scheduled {
		s.recordResponseSnapshotStoreFailure(failure, errors.New("server shutting down, snapshot write skipped"))
	}
}

// goSnapshotWrite runs fn as a tracked background snapshot write. It reports
// false without running fn when draining has begun: registering a write after
// drainSnapshotWrites starts waiting would race the WaitGroup and could touch
// a store that shutdown already closed. The read lock is held across the
// WaitGroup registration so the drain cannot observe it as untracked.
func (s *translatedInferenceService) goSnapshotWrite(fn func()) bool {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	if s.snapshotDraining {
		return false
	}
	s.snapshotWrites.Go(fn)
	return true
}

// drainSnapshotWrites stops accepting new background snapshot writes and
// blocks until every in-flight one finishes. The server calls it during
// shutdown, before the response store closes; writes attempted afterwards are
// skipped and recorded as store failures.
func (s *translatedInferenceService) drainSnapshotWrites() {
	s.snapshotMu.Lock()
	s.snapshotDraining = true
	s.snapshotMu.Unlock()
	s.snapshotWrites.Wait()
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

// snapshotFailureRecord carries the identifiers a snapshot-write failure is
// reported with. All fields are plain strings captured on the request path so
// the background goroutine never touches request-owned structs.
type snapshotFailureRecord struct {
	providerType      string
	providerName      string
	requestID         string
	workflowVersionID string
	responseID        string
}

func (s *translatedInferenceService) recordResponseSnapshotStoreFailure(rec snapshotFailureRecord, err error) {
	observability.ResponseSnapshotStoreFailures.WithLabelValues(
		strings.TrimSpace(rec.providerType),
		strings.TrimSpace(rec.providerName),
		"store",
	).Inc()

	slog.Warn("response snapshot store failed",
		"request_id", rec.requestID,
		"provider_type", rec.providerType,
		"provider_name", rec.providerName,
		"workflow_version_id", rec.workflowVersionID,
		"response_id", strings.TrimSpace(rec.responseID),
		"error", err,
	)
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

	adm, err := enforceAdmission(c, s.rateLimiter, s.budgetChecker, rateLimitRouteFromWorkflow(prepared.Workflow))
	if err != nil {
		return handleError(c, err)
	}
	defer adm.release()

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

// observeLiveProviderAttempts surfaces provider attempts in the live audit
// preview as they are recorded (e.g. a failed primary while failover is still
// in flight), instead of only once the request finishes. It installs the
// observer only when failover targets exist, so non-failover requests — the hot
// path — take on no extra per-request work.
func (s *translatedInferenceService) observeLiveProviderAttempts(c *echo.Context, workflow *core.Workflow) {
	if len(s.inference().FailoverSelectors(workflow)) == 0 {
		return
	}
	req := c.Request()
	c.SetRequest(req.WithContext(gateway.WithAttemptObserver(req.Context(), func() {
		enrichAuditEntryWithProviderAttempts(c)
	})))
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
	meta gateway.ExecutionMeta,
	stream io.ReadCloser,
	outerWrap func(io.ReadCloser) io.ReadCloser,
) error {
	model, provider, providerName := meta.Model, meta.ProviderType, meta.ProviderName
	auditlog.MarkEntryAsStreaming(c, true)
	auditlog.EnrichEntryWithStream(c, true)
	enrichAuditEntryWithProviderAttempts(c)
	auditlog.EnrichEntryWithFailover(c, meta.FailoverModel)
	auditlog.EnrichEntryWithResolvedRoute(c, qualifyExecutedModel(workflow, model, providerName), provider, providerName)

	entry := auditlog.GetStreamEntryFromContext(c)
	auditEnabled := s.logger != nil && s.logger.Config().Enabled && (workflow == nil || workflow.AuditEnabled())
	if auditEnabled && entry != nil {
		auditlog.PopulateRequestData(entry, c.Request(), s.logger.Config())
	}
	streamEntry := auditlog.CreateStreamEntry(c.Request().Context(), entry)
	if streamEntry != nil {
		streamEntry.StatusCode = http.StatusOK
	}

	requestID := requestIDFromContextOrHeader(c.Request())
	endpoint := c.Request().URL.Path
	observers := make([]streaming.Observer, 0, 3)
	if auditEnabled && streamEntry != nil {
		observers = append(observers, auditlog.NewStreamLogObserver(s.logger, streamEntry, endpoint))
	}
	if s.usageLogger != nil && s.usageLogger.Config().Enabled && (workflow == nil || workflow.UsageEnabled()) {
		usageObserver := usage.NewStreamUsageObserver(s.usageLogger, model, provider, requestID, endpoint, s.pricingResolver, core.UserPathFromContext(c.Request().Context()))
		if usageObserver != nil {
			usageObserver.SetProviderName(providerName)
			usageObserver.SetSessionID(core.SessionIDFromContext(c.Request().Context()))
			usageObserver.SetLabels(core.RequestLabelsFromContext(c.Request().Context()))
			usageObserver.SetRewriteTokensSaved(core.RewriteTokensSavedFromContext(c.Request().Context()))
			observers = append(observers, usageObserver)
		}
	}
	if hasResponseFeedbackObservers(c) {
		observers = append(observers, &responseFeedbackStreamObserver{
			ctx:          c.Request().Context(),
			observers:    responseFeedbackObservers(c),
			requestID:    requestID,
			sessionID:    core.SessionIDFromContext(c.Request().Context()),
			endpoint:     ext.Endpoint(endpoint),
			model:        model,
			providerType: provider,
			providerName: providerName,
		})
	}
	wrappedStream := streaming.NewObservedSSEStream(stream, observers...)
	if outerWrap != nil {
		wrappedStream = outerWrap(wrappedStream)
	}

	defer func() {
		_ = wrappedStream.Close() //nolint:errcheck
	}()

	applyPluginResponseHeaders(c)
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

func qualifyExecutedModel(workflow *core.Workflow, model, providerName string) string {
	return gateway.QualifyExecutedModel(workflow, model, providerName)
}

func markRequestFailoverUsed(c *echo.Context) {
	if c == nil || c.Request() == nil {
		return
	}
	c.SetRequest(c.Request().WithContext(core.WithFailoverUsed(c.Request().Context())))
}

func resolvedModelFromWorkflow(workflow *core.Workflow, fallback string) string {
	return gateway.ResolvedModelFromWorkflow(workflow, fallback)
}

func marshalRequestBody(req any) ([]byte, error) {
	return json.Marshal(req)
}
