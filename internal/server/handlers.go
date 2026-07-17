// Package server provides HTTP handlers and server setup for the LLM gateway.
package server

import (
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/auditlog"
	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Handler holds the HTTP handlers
type Handler struct {
	provider                        core.RoutableProvider
	modelResolver                   RequestModelResolver
	modelAuthorizer                 RequestModelAuthorizer
	failoverResolver                RequestFailoverResolver
	workflowPolicyResolver          RequestWorkflowPolicyResolver
	translatedRequestPatcher        TranslatedRequestPatcher
	batchRequestPreparer            BatchRequestPreparer
	exposedModelLister              ExposedModelLister
	keepOnlyAliasesAtModelsEndpoint bool
	logger                          auditlog.LoggerInterface
	usageLogger                     usage.LoggerInterface
	budgetChecker                   BudgetChecker
	rateLimiter                     RateLimiter
	usageSummarizer                 UsageSummarizer
	userPathHeaderName              string
	pricingResolver                 usage.PricingResolver
	batchStore                      batchstore.Store
	fileStore                       filestore.Store
	responseStore                   responsestore.Store
	// storesMu guards responseStore, conversationStore, and translatedSvc wiring.
	storesMu                     sync.RWMutex
	conversationStore            conversationstore.Store
	normalizePassthroughV1Prefix bool
	enabledPassthroughProviders  map[string]struct{}
	realtimeEnabled              bool
	mcpEnabled                   bool
	mcpGateway                   *mcpgateway.Service
	realtimeCalls                *realtime.CallRegistry
	realtimeHTTPClient           *http.Client
	responseCache                *responsecache.ResponseCacheMiddleware
	guardrailsHash               string
	storageProbe                 ReadinessProbe
	cacheProbe                   ReadinessProbe

	translatedSvc     *translatedInferenceService // snapshot of handler fields at first use; server.New sets cache/hash before traffic
	translatedSvcOnce sync.Once
}

// newHandlerWithAuthorizer creates a new handler with the given routable
// provider (typically the Router) and optional resolvers.
func newHandlerWithAuthorizer(
	provider core.RoutableProvider,
	logger auditlog.LoggerInterface,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
	modelResolver RequestModelResolver,
	modelAuthorizer RequestModelAuthorizer,
	workflowPolicyResolver RequestWorkflowPolicyResolver,
	failoverResolver RequestFailoverResolver,
	translatedRequestPatcher TranslatedRequestPatcher,
) *Handler {
	return &Handler{
		provider:                 provider,
		modelResolver:            modelResolver,
		modelAuthorizer:          modelAuthorizer,
		failoverResolver:         failoverResolver,
		workflowPolicyResolver:   workflowPolicyResolver,
		translatedRequestPatcher: translatedRequestPatcher,
		logger:                   logger,
		usageLogger:              usageLogger,
		pricingResolver:          pricingResolver,
		batchStore:               batchstore.NewMemoryStore(),
		fileStore:                filestore.NewMemoryStore(),
		// Fallback stores with default bounded retention (TTL plus entry and
		// byte caps); app wiring replaces them with storage-backed stores.
		responseStore:                responsestore.NewMemoryStore(),
		conversationStore:            conversationstore.NewMemoryStore(),
		normalizePassthroughV1Prefix: true,
		enabledPassthroughProviders:  normalizeEnabledPassthroughProviders(defaultEnabledPassthroughProviders),
		realtimeCalls:                realtime.NewCallRegistry(),
		realtimeHTTPClient:           httpclient.NewDefaultHTTPClient(),
	}
}

// SetBatchStore replaces the batch store used by lifecycle endpoints.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetBatchStore(store batchstore.Store) {
	if store == nil {
		return
	}
	h.batchStore = store
}

// SetFileStore replaces the file provider mapping store.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetFileStore(store filestore.Store) {
	if store == nil {
		return
	}
	h.fileStore = store
}

// SetResponseStore replaces the response snapshot store used by lifecycle endpoints.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetResponseStore(store responsestore.Store) {
	if store == nil {
		return
	}
	h.storesMu.Lock()
	defer h.storesMu.Unlock()
	h.responseStore = store
	if h.translatedSvc != nil {
		h.translatedSvc.setResponseStore(store)
	}
}

// SetConversationStore replaces the conversation store used by the
// Conversations lifecycle endpoints and by /v1/responses conversation turns.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetConversationStore(store conversationstore.Store) {
	if store == nil {
		return
	}
	h.storesMu.Lock()
	defer h.storesMu.Unlock()
	h.conversationStore = store
	if h.translatedSvc != nil {
		h.translatedSvc.setConversationStore(store)
	}
}

func (h *Handler) translatedInference() *translatedInferenceService {
	h.translatedSvcOnce.Do(func() {
		s := &translatedInferenceService{
			provider:                 h.provider,
			modelResolver:            h.modelResolver,
			modelAuthorizer:          h.modelAuthorizer,
			workflowPolicyResolver:   h.workflowPolicyResolver,
			failoverResolver:         h.failoverResolver,
			translatedRequestPatcher: h.translatedRequestPatcher,
			logger:                   h.logger,
			usageLogger:              h.usageLogger,
			budgetChecker:            h.budgetChecker,
			rateLimiter:              h.rateLimiter,
			pricingResolver:          h.pricingResolver,
			responseCache:            h.responseCache,
			guardrailsHash:           h.guardrailsHash,
			responseStore:            h.currentResponseStore(),
		}
		s.initHandlers()
		h.storesMu.Lock()
		s.setResponseStore(h.responseStore)
		s.setConversationStore(h.conversationStore)
		h.translatedSvc = s
		h.storesMu.Unlock()
	})
	h.storesMu.RLock()
	defer h.storesMu.RUnlock()
	return h.translatedSvc
}

func (h *Handler) nativeBatch() *nativeBatchService {
	return &nativeBatchService{
		provider:                             h.provider,
		modelResolver:                        h.modelResolver,
		modelAuthorizer:                      h.modelAuthorizer,
		inputFileProviderResolver:            newBatchInputFileProviderResolver(h.provider, h.fileStore),
		workflowPolicyResolver:               h.workflowPolicyResolver,
		batchRequestPreparer:                 h.batchRequestPreparer,
		batchStore:                           h.batchStore,
		cleanupPreparedBatchInputFile:        h.cleanupPreparedBatchInputFile,
		cleanupStoredBatchRewrittenInputFile: h.cleanupStoredBatchRewrittenInputFile,
		usageLogger:                          h.usageLogger,
		budgetChecker:                        h.budgetChecker,
		rateLimiter:                          h.rateLimiter,
		pricingResolver:                      h.pricingResolver,
	}
}

func (h *Handler) nativeFiles() *nativeFileService {
	return &nativeFileService{provider: h.provider, fileStore: h.fileStore}
}

func (h *Handler) audio() *audioService {
	var logBodies, logAudioBodies bool
	if h.logger != nil {
		cfg := h.logger.Config()
		logBodies = cfg.LogBodies
		logAudioBodies = cfg.LogAudioBodies
	}
	return &audioService{
		provider:        h.provider,
		modelResolver:   h.modelResolver,
		modelAuthorizer: h.modelAuthorizer,
		budgetChecker:   h.budgetChecker,
		rateLimiter:     h.rateLimiter,
		logBodies:       logBodies,
		logAudioBodies:  logAudioBodies,
		usageLogger:     h.usageLogger,
		pricingResolver: h.pricingResolver,
	}
}

func (h *Handler) nativeResponses() *nativeResponseService {
	return &nativeResponseService{
		provider:                 h.provider,
		modelResolver:            h.modelResolver,
		modelAuthorizer:          h.modelAuthorizer,
		workflowPolicyResolver:   h.workflowPolicyResolver,
		translatedRequestPatcher: h.translatedRequestPatcher,
		responseStore:            h.currentResponseStore(),
	}
}

func (h *Handler) conversations() *conversationService {
	h.storesMu.RLock()
	defer h.storesMu.RUnlock()
	return &conversationService{conversationStore: h.conversationStore}
}

func (h *Handler) currentResponseStore() responsestore.Store {
	h.storesMu.RLock()
	defer h.storesMu.RUnlock()
	return h.responseStore
}

func (h *Handler) realtime() *realtimeService {
	return &realtimeService{
		provider:        h.provider,
		modelResolver:   h.modelResolver,
		modelAuthorizer: h.modelAuthorizer,
		budgetChecker:   h.budgetChecker,
		rateLimiter:     h.rateLimiter,
		usageLogger:     h.usageLogger,
		pricingResolver: h.pricingResolver,
		calls:           h.realtimeCalls,
		httpClient:      h.realtimeHTTPClient,
		enabled:         h.realtimeEnabled,
	}
}

func (h *Handler) mcp() *mcpService {
	var logBodies bool
	if h.logger != nil {
		logBodies = h.logger.Config().LogBodies
	}
	return &mcpService{
		gateway:       h.mcpGateway,
		budgetChecker: h.budgetChecker,
		rateLimiter:   h.rateLimiter,
		enabled:       h.mcpEnabled && h.mcpGateway != nil,
		logBodies:     logBodies,
	}
}

func (h *Handler) passthrough() *passthroughService {
	return &passthroughService{
		provider:                     h.provider,
		modelAuthorizer:              h.modelAuthorizer,
		logger:                       h.logger,
		usageLogger:                  h.usageLogger,
		budgetChecker:                h.budgetChecker,
		rateLimiter:                  h.rateLimiter,
		pricingResolver:              h.pricingResolver,
		normalizePassthroughV1Prefix: h.normalizePassthroughV1Prefix,
		enabledPassthroughProviders:  h.enabledPassthroughProviders,
	}
}

// ProviderPassthrough handles opaque provider-native requests under /p/{provider}/{endpoint}.
//
// OpenAI and Anthropic are the first-class providers in this ADR-0002 slice. Other
// providers are intentionally deferred until they fit the same low-friction opaque path.
//
// @Summary      Provider passthrough
// @Description  Runtime-configurable passthrough endpoint under /p/{provider}/{endpoint}; enabled by default via server.enable_passthrough_routes. The endpoint path is opaque and may proxy JSON, binary, or SSE responses with upstream status codes preserved. For multi-segment provider endpoints, clients that rely on OpenAPI-generated path handling should URL-encode embedded slashes in the endpoint parameter. A leading v1/ segment is normalized away by default so /p/{provider}/v1/... and /p/{provider}/... map to the same upstream path relative to the provider base URL.
// @Tags         passthrough
// @Accept       json
// @Accept       mpfd
// @Produce      json
// @Produce      application/octet-stream
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        provider  path      string  true  "Provider type"
// @Param        endpoint  path      string  true  "Provider-native endpoint path relative to the provider base URL. URL-encode embedded / characters when using generated clients."
// @Success      200       {file}    file    "Opaque upstream response body"
// @Success      201       {file}    file    "Opaque upstream response body"
// @Success      202       {file}    file    "Opaque upstream response body"
// @Success      204       {string}  string  "No Content passthrough response"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /p/{provider}/{endpoint} [get]
// @Router       /p/{provider}/{endpoint} [post]
// @Router       /p/{provider}/{endpoint} [put]
// @Router       /p/{provider}/{endpoint} [patch]
// @Router       /p/{provider}/{endpoint} [delete]
// @Router       /p/{provider}/{endpoint} [head]
// @Router       /p/{provider}/{endpoint} [options]
func (h *Handler) ProviderPassthrough(c *echo.Context) error {
	// A websocket upgrade on a passthrough route is a realtime session, not an
	// HTTP proxy request; relay it through the realtime service instead.
	if isWebSocketUpgrade(c.Request()) {
		providerType, _, endpoint, _, err := passthroughExecutionTarget(c, h.provider, h.normalizePassthroughV1Prefix)
		if err != nil {
			return handleError(c, err)
		}
		// Realtime upgrades honor the same provider allowlist as the HTTP
		// passthrough path: a provider disabled for /p/{provider}/... must not be
		// reachable via a websocket upgrade.
		if !isEnabledPassthroughProvider(providerType, h.enabledPassthroughProviders) {
			return handleError(c, h.passthrough().unsupportedPassthroughProviderError(providerType))
		}
		// endpoint may carry the query string (e.g. "realtime?model=..."); compare
		// only the path segment.
		endpointPath := strings.Trim(strings.SplitN(endpoint, "?", 2)[0], "/")
		if endpointPath != "realtime" {
			return handleError(c, core.NewNotFoundError("unsupported realtime passthrough endpoint: "+endpointPath))
		}
		return h.realtime().PassthroughRealtime(c, providerType)
	}
	return h.passthrough().ProviderPassthrough(c)
}

// Realtime handles GET /v1/realtime.
//
// @Summary      Open a realtime session
// @Description  Upgrades to a websocket and relays an OpenAI-compatible realtime (speech-to-speech) session to the provider that owns the model named in the ?model= query parameter. Provider credentials are injected by the gateway. Passing ?call_id= instead attaches to an existing WebRTC/SIP call as a sideband channel; calls created through this gateway instance are routed automatically, others need explicit model (and provider) parameters.
// @Tags         realtime
// @Security     BearerAuth
// @Param        model     query     string  false  "Model that owns the realtime session (required unless call_id names a call created through this gateway instance)"
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        call_id   query     string  false  "Existing WebRTC/SIP call to attach to as a sideband channel"
// @Success      101       {string}  string  "Switching Protocols"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime [get]
func (h *Handler) Realtime(c *echo.Context) error {
	return h.realtime().Realtime(c)
}

// MCP handles the aggregated MCP endpoint at /mcp.
//
// @Summary      MCP gateway (aggregated)
// @Description  Streamable-HTTP MCP endpoint aggregating every configured upstream MCP server visible to the caller. Tools and prompts are namespaced as {slug}_{name}. POST carries JSON-RPC messages, GET opens the server-notification SSE stream, DELETE ends the session. The X-MCP-Servers request header optionally narrows the visible servers to a comma-separated subset of server slugs.
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Success      200  {object}  map[string]interface{}  "JSON-RPC response or SSE stream"
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      429  {object}  core.OpenAIErrorEnvelope
// @Failure      501  {object}  core.OpenAIErrorEnvelope
// @Router       /mcp [post]
// @Router       /mcp [get]
// @Router       /mcp [delete]
func (h *Handler) MCP(c *echo.Context) error {
	return h.mcp().handle(c, "")
}

// MCPServer handles the per-server MCP endpoints at /mcp/{server}.
//
// @Summary      MCP gateway (single server)
// @Description  Streamable-HTTP MCP endpoint exposing one configured upstream MCP server with original (un-prefixed) tool names.
// @Tags         mcp
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        server  path      string  true  "Configured MCP server slug"
// @Success      200     {object}  map[string]interface{}  "JSON-RPC response or SSE stream"
// @Failure      401     {object}  core.OpenAIErrorEnvelope
// @Failure      404     {object}  core.OpenAIErrorEnvelope
// @Failure      429     {object}  core.OpenAIErrorEnvelope
// @Failure      501     {object}  core.OpenAIErrorEnvelope
// @Router       /mcp/{server} [post]
// @Router       /mcp/{server} [get]
// @Router       /mcp/{server} [delete]
func (h *Handler) MCPServer(c *echo.Context) error {
	return h.mcp().handle(c, c.Param("server"))
}

// RealtimeCalls handles POST /v1/realtime/calls.
//
// @Summary      Create a realtime WebRTC call
// @Description  OpenAI-compatible WebRTC SDP exchange. Accepts a raw application/sdp offer with the model in the ?model= query parameter, or a multipart form with sdp and session (JSON) fields. The gateway routes by model, injects provider credentials, and relays the SDP answer; the Location header carries the created call id. Media flows directly between the client and the provider, so usage is recorded by a gateway-side sideband observer when usage tracking is enabled.
// @Tags         realtime
// @Accept       application/sdp
// @Accept       mpfd
// @Produce      application/sdp
// @Produce      json
// @Security     BearerAuth
// @Param        model     query     string  false  "Model that owns the call (required for application/sdp offers)"
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      string  true   "SDP offer (raw application/sdp body), or a multipart form with sdp and session (JSON) fields"
// @Success      201       {string}  string  "SDP answer"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/calls [post]
func (h *Handler) RealtimeCalls(c *echo.Context) error {
	return h.realtime().RealtimeCalls(c)
}

// RealtimeClientSecrets handles POST /v1/realtime/client_secrets.
//
// @Summary      Mint an ephemeral realtime client secret
// @Description  OpenAI-compatible ephemeral credential minting for browser and mobile realtime clients. Routes by session.model (or the transcription model for transcription sessions), applies the same model-access, budget, and rate-limit gates as other model endpoints, and relays the provider response verbatim. The minted secret authenticates the client directly against the provider, bypassing the gateway for the session itself.
// @Tags         realtime
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Optional provider hint"
// @Param        request   body      object{session=object,expires_after=object}  true  "Client secret request: session config with the routing model (session.model, or the nested transcription model) plus optional expires_after; additional fields are relayed verbatim"
// @Success      200       {object}  map[string]any  "Provider client secret response"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      429       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/realtime/client_secrets [post]
func (h *Handler) RealtimeClientSecrets(c *echo.Context) error {
	return h.realtime().RealtimeClientSecrets(c)
}

// ChatCompletion handles POST /v1/chat/completions
//
// @Summary      Create a chat completion
// @Tags         chat
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      core.ChatRequest  true  "Chat completion request"
// @Success      200      {object}  core.ChatResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/chat/completions [post]
func (h *Handler) ChatCompletion(c *echo.Context) error {
	return h.translatedInference().ChatCompletion(c)
}

// Health handles GET /health
//
// @Summary      Health check
// @Tags         system
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /health [get]
func (h *Handler) Health(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ListModels handles GET /v1/models
//
// @Summary      List available models
// @Tags         models
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  core.ModelsResponse
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/models [get]
func (h *Handler) ListModels(c *echo.Context) error {
	// Create context with request ID for provider
	requestID := c.Request().Header.Get("X-Request-ID")
	ctx := core.WithRequestID(c.Request().Context(), requestID)

	resp, err := h.provider.ListModels(ctx)
	if err != nil {
		return handleError(c, err)
	}
	if h.keepOnlyAliasesAtModelsEndpoint {
		object := "list"
		if resp != nil && resp.Object != "" {
			object = resp.Object
		}
		resp = &core.ModelsResponse{Object: object, Data: []core.Model{}}
	}
	if h.modelAuthorizer != nil && resp != nil {
		resp = &core.ModelsResponse{
			Object: resp.Object,
			Data:   h.modelAuthorizer.FilterPublicModels(c.Request().Context(), resp.Data),
		}
	}
	if h.exposedModelLister != nil {
		ctx := c.Request().Context()
		// The target-access filter is only available when an authorizer is set.
		var allow func(core.ModelSelector) bool
		if h.modelAuthorizer != nil {
			allow = func(selector core.ModelSelector) bool {
				return h.modelAuthorizer.AllowsModel(ctx, selector)
			}
		}
		// User-path scoping of redirects is a property of the redirect itself, not
		// of the authorizer, so it must apply even when no authorizer is configured
		// (allow is nil there) — otherwise scoped redirect IDs leak to callers
		// outside their user_paths.
		if scoped, ok := h.exposedModelLister.(UserPathExposedModelLister); ok {
			resp = mergeExposedModelsResponse(resp, scoped.ExposedModelsForUserPath(core.UserPathFromContext(ctx), allow))
		} else if filtered, ok := h.exposedModelLister.(FilteredExposedModelLister); ok && allow != nil {
			resp = mergeExposedModelsResponse(resp, filtered.ExposedModelsFiltered(allow))
		} else {
			exposed := h.exposedModelLister.ExposedModels()
			if allow != nil {
				filtered := make([]core.Model, 0, len(exposed))
				for _, model := range exposed {
					selector, err := core.ParseModelSelector(model.ID, "")
					if err != nil || !allow(selector) {
						continue
					}
					filtered = append(filtered, model)
				}
				exposed = filtered
			}
			resp = mergeExposedModelsResponse(resp, exposed)
		}
	}

	// The models route is shared by both wire dialects. Anthropic SDK clients
	// are identified by the anthropic-version header they always send; render
	// the Anthropic list shape for them, the OpenAI shape for everyone else.
	if c.Request().Header.Get("anthropic-version") != "" {
		var models []core.Model
		if resp != nil {
			models = resp.Data
		}
		return c.JSON(http.StatusOK, anthropicapi.FromModels(models))
	}

	return c.JSON(http.StatusOK, resp)
}

// CreateFile handles POST /v1/files.
//
// @Summary      Upload a file
// @Tags         files
// @Accept       mpfd
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider override when multiple providers are configured"
// @Param        purpose   formData  string  true   "File purpose"
// @Param        file      formData  file    true   "File to upload"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files [post]
func (h *Handler) CreateFile(c *echo.Context) error {
	return h.nativeFiles().CreateFile(c)
}

// ListFiles handles GET /v1/files.
//
// @Summary      List files
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        provider  query     string  false  "Provider filter"
// @Param        purpose   query     string  false  "File purpose filter"
// @Param        after     query     string  false  "Pagination cursor"
// @Param        limit     query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200       {object}  core.FileListResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files [get]
func (h *Handler) ListFiles(c *echo.Context) error {
	return h.nativeFiles().ListFiles(c)
}

// GetFile handles GET /v1/files/{id}.
//
// @Summary      Get file metadata
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileObject
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id} [get]
func (h *Handler) GetFile(c *echo.Context) error {
	return h.nativeFiles().GetFile(c)
}

// DeleteFile handles DELETE /v1/files/{id}.
//
// @Summary      Delete a file
// @Tags         files
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "File ID"
// @Param        provider  query     string  false  "Provider override"
// @Success      200       {object}  core.FileDeleteResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id} [delete]
func (h *Handler) DeleteFile(c *echo.Context) error {
	return h.nativeFiles().DeleteFile(c)
}

// GetFileContent handles GET /v1/files/{id}/content.
//
// @Summary      Download file content
// @Tags         files
// @Produce      application/octet-stream
// @Security     BearerAuth
// @Param        id        path   string  true   "File ID"
// @Param        provider  query  string  false  "Provider override"
// @Success      200       {file}  file  "Raw file content"
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/files/{id}/content [get]
func (h *Handler) GetFileContent(c *echo.Context) error {
	return h.nativeFiles().GetFileContent(c)
}

// AudioSpeech handles POST /v1/audio/speech.
//
// @Summary      Create speech (text-to-speech)
// @Tags         audio
// @Accept       json
// @Produce      application/octet-stream
// @Security     BearerAuth
// @Param        request  body      core.AudioSpeechRequest  true  "Text-to-speech request"
// @Success      200      {file}    file  "Binary audio in the requested response_format"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/speech [post]
func (h *Handler) AudioSpeech(c *echo.Context) error {
	return h.audio().CreateSpeech(c)
}

// AudioTranscriptions handles POST /v1/audio/transcriptions.
//
// @Summary      Create transcription (speech-to-text)
// @Tags         audio
// @Accept       mpfd
// @Produce      json
// @Produce      plain
// @Security     BearerAuth
// @Param        file             formData  file    true   "Audio file to transcribe"
// @Param        model            formData  string  true   "Model ID"
// @Param        language         formData  string  false  "Input language (ISO-639-1)"
// @Param        prompt           formData  string  false  "Optional text to guide the model"
// @Param        response_format          formData  string    false  "json, text, srt, verbose_json, or vtt"
// @Param        temperature              formData  number    false  "Sampling temperature (0-1)"
// @Param        timestamp_granularities[] formData  []string  false  "Timestamp granularities to populate: word and/or segment"
// @Success      200                      {object}  map[string]interface{}  "Transcription in the requested response_format: a JSON object for json/verbose_json, or a text/plain body for text/srt/vtt"
// @Failure      400              {object}  core.OpenAIErrorEnvelope
// @Failure      401              {object}  core.OpenAIErrorEnvelope
// @Failure      404              {object}  core.OpenAIErrorEnvelope
// @Failure      502              {object}  core.OpenAIErrorEnvelope
// @Router       /v1/audio/transcriptions [post]
func (h *Handler) AudioTranscriptions(c *echo.Context) error {
	return h.audio().CreateTranscription(c)
}

// Responses handles POST /v1/responses
//
// @Summary      Create a model response (Responses API)
// @Tags         responses
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      core.ResponsesRequest  true  "Responses API request"
// @Success      200      {object}  core.ResponsesResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses [post]
func (h *Handler) Responses(c *echo.Context) error {
	return h.translatedInference().Responses(c)
}

// GetResponse handles GET /v1/responses/{id}.
//
// @Summary      Get a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native lookups"
// @Param        include   query     []string false "Fields to include in the response" collectionFormat(multi)
// @Param        include[] query     []string false "Fields to include in the response" collectionFormat(multi)
// @Param        include_obfuscation query bool false "Whether to include obfuscated response data"
// @Param        starting_after query int false "Input item offset for providers that support it"
// @Success      200       {object}  core.ResponsesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id} [get]
func (h *Handler) GetResponse(c *echo.Context) error {
	return h.nativeResponses().GetResponse(c)
}

// ListResponseInputItems handles GET /v1/responses/{id}/input_items.
//
// @Summary      List response input items
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native lookups"
// @Param        after     query     string  false  "Pagination cursor"
// @Param        include   query     []string false "Fields to include in listed input items" collectionFormat(multi)
// @Param        include[] query     []string false "Fields to include in listed input items" collectionFormat(multi)
// @Param        limit     query     int     false  "Maximum items to return (1-100, default 20)"
// @Param        order     query     string  false  "Sort order: asc or desc"  Enums(asc, desc)
// @Success      200       {object}  core.ResponseInputItemListResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id}/input_items [get]
func (h *Handler) ListResponseInputItems(c *echo.Context) error {
	return h.nativeResponses().ListResponseInputItems(c)
}

// CancelResponse handles POST /v1/responses/{id}/cancel.
//
// @Summary      Cancel a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native cancellation"
// @Success      200       {object}  core.ResponsesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id}/cancel [post]
func (h *Handler) CancelResponse(c *echo.Context) error {
	return h.nativeResponses().CancelResponse(c)
}

// DeleteResponse handles DELETE /v1/responses/{id}.
//
// @Summary      Delete a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native deletion"
// @Success      200       {object}  core.ResponseDeleteResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id} [delete]
func (h *Handler) DeleteResponse(c *echo.Context) error {
	return h.nativeResponses().DeleteResponse(c)
}

// ResponseInputTokens handles POST /v1/responses/input_tokens.
//
// @Summary      Count response input tokens
// @Tags         responses
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ResponseInputTokensRequest  true  "Response input token request"
// @Success      200      {object}  core.ResponseInputTokensResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      501      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/input_tokens [post]
func (h *Handler) ResponseInputTokens(c *echo.Context) error {
	return h.nativeResponses().CountResponseInputTokens(c)
}

// CompactResponse handles POST /v1/responses/compact.
//
// @Summary      Compact response input
// @Tags         responses
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ResponseCompactRequest  true  "Response compact request"
// @Success      200      {object}  core.ResponseCompactResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      501      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/compact [post]
func (h *Handler) CompactResponse(c *echo.Context) error {
	return h.nativeResponses().CompactResponse(c)
}

// Embeddings handles POST /v1/embeddings
//
// @Summary      Create embeddings
// @Tags         embeddings
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.EmbeddingRequest  true  "Embeddings request"
// @Success      200      {object}  core.EmbeddingResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/embeddings [post]
func (h *Handler) Embeddings(c *echo.Context) error {
	return h.translatedInference().Embeddings(c)
}

// Batches handles POST /v1/batches.
//
// OpenAI-compatible fields are accepted (`input_file_id`, `endpoint`, `completion_window`, `metadata`).
// Inline `requests` are also accepted for providers with native inline batch support (for example Anthropic).
//
// @Summary      Create a native provider batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.BatchRequest  true  "Batch request"
// @Success      200      {object}  core.BatchResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches [post]
func (h *Handler) Batches(c *echo.Context) error {
	return h.nativeBatch().Batches(c)
}

// GetBatch handles GET /v1/batches/{id}.
//
// @Summary      Get a batch
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id} [get]
func (h *Handler) GetBatch(c *echo.Context) error {
	return h.nativeBatch().GetBatch(c)
}

// ListBatches handles GET /v1/batches.
//
// @Summary      List batches
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        after  query     string  false  "Pagination cursor"
// @Param        limit  query     int     false  "Maximum items to return (1-100, default 20)"
// @Success      200    {object}  core.BatchListResponse
// @Failure      400    {object}  core.OpenAIErrorEnvelope
// @Failure      401    {object}  core.OpenAIErrorEnvelope
// @Failure      404    {object}  core.OpenAIErrorEnvelope
// @Failure      500    {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches [get]
func (h *Handler) ListBatches(c *echo.Context) error {
	return h.nativeBatch().ListBatches(c)
}

// CancelBatch handles POST /v1/batches/{id}/cancel.
//
// @Summary      Cancel a batch
// @Tags         batch
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id}/cancel [post]
func (h *Handler) CancelBatch(c *echo.Context) error {
	return h.nativeBatch().CancelBatch(c)
}

// BatchResults handles GET /v1/batches/{id}/results.
//
// @Summary      Get batch results
// @Tags         batch
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      string  true  "Batch ID"
// @Success      200  {object}  core.BatchResultsResponse
// @Failure      400  {object}  core.OpenAIErrorEnvelope
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      404  {object}  core.OpenAIErrorEnvelope
// @Failure      409  {object}  core.OpenAIErrorEnvelope
// @Failure      500  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/batches/{id}/results [get]
func (h *Handler) BatchResults(c *echo.Context) error {
	return h.nativeBatch().BatchResults(c)
}
