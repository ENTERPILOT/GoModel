// Package server provides HTTP handlers and server setup for the LLM gateway.
package server

import (
	"net/http"
	"sync"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/responsecache"
	"gomodel/internal/responsestore"
	"gomodel/internal/usage"
)

// Handler holds the HTTP handlers
type Handler struct {
	provider                        core.RoutableProvider
	modelResolver                   RequestModelResolver
	modelAuthorizer                 RequestModelAuthorizer
	fallbackResolver                RequestFallbackResolver
	workflowPolicyResolver          RequestWorkflowPolicyResolver
	translatedRequestPatcher        TranslatedRequestPatcher
	batchRequestPreparer            BatchRequestPreparer
	exposedModelLister              ExposedModelLister
	keepOnlyAliasesAtModelsEndpoint bool
	logger                          auditlog.LoggerInterface
	usageLogger                     usage.LoggerInterface
	budgetChecker                   BudgetChecker
	pricingResolver                 usage.PricingResolver
	batchStore                      batchstore.Store
	responseStore                   responsestore.Store
	responseStoreMu                 sync.RWMutex
	normalizePassthroughV1Prefix    bool
	enabledPassthroughProviders     map[string]struct{}
	responseCache                   *responsecache.ResponseCacheMiddleware
	guardrailsHash                  string

	translatedSvc     *translatedInferenceService // snapshot of handler fields at first use; server.New sets cache/hash before traffic
	translatedSvcOnce sync.Once
}

// NewHandler creates a new handler with the given routable provider (typically the Router)
func NewHandler(provider core.RoutableProvider, logger auditlog.LoggerInterface, usageLogger usage.LoggerInterface, pricingResolver usage.PricingResolver) *Handler {
	return newHandler(provider, logger, usageLogger, pricingResolver, nil, nil, nil, nil)
}

func newHandler(
	provider core.RoutableProvider,
	logger auditlog.LoggerInterface,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
	modelResolver RequestModelResolver,
	workflowPolicyResolver RequestWorkflowPolicyResolver,
	fallbackResolver RequestFallbackResolver,
	translatedRequestPatcher TranslatedRequestPatcher,
) *Handler {
	return newHandlerWithAuthorizer(
		provider,
		logger,
		usageLogger,
		pricingResolver,
		modelResolver,
		nil,
		workflowPolicyResolver,
		fallbackResolver,
		translatedRequestPatcher,
	)
}

func newHandlerWithAuthorizer(
	provider core.RoutableProvider,
	logger auditlog.LoggerInterface,
	usageLogger usage.LoggerInterface,
	pricingResolver usage.PricingResolver,
	modelResolver RequestModelResolver,
	modelAuthorizer RequestModelAuthorizer,
	workflowPolicyResolver RequestWorkflowPolicyResolver,
	fallbackResolver RequestFallbackResolver,
	translatedRequestPatcher TranslatedRequestPatcher,
) *Handler {
	return &Handler{
		provider:                 provider,
		modelResolver:            modelResolver,
		modelAuthorizer:          modelAuthorizer,
		fallbackResolver:         fallbackResolver,
		workflowPolicyResolver:   workflowPolicyResolver,
		translatedRequestPatcher: translatedRequestPatcher,
		logger:                   logger,
		usageLogger:              usageLogger,
		pricingResolver:          pricingResolver,
		batchStore:               batchstore.NewMemoryStore(),
		responseStore: responsestore.NewMemoryStore(
			responsestore.WithTTL(responsestore.DefaultMemoryStoreTTL),
			responsestore.WithMaxEntries(responsestore.DefaultMemoryStoreMaxEntries),
		),
		normalizePassthroughV1Prefix: true,
		enabledPassthroughProviders:  normalizeEnabledPassthroughProviders(defaultEnabledPassthroughProviders),
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

// SetResponseStore replaces the response snapshot store used by lifecycle endpoints.
// nil is ignored to keep an always-available fallback memory store.
func (h *Handler) SetResponseStore(store responsestore.Store) {
	if store == nil {
		return
	}
	h.responseStoreMu.Lock()
	defer h.responseStoreMu.Unlock()
	h.responseStore = store
	if h.translatedSvc != nil {
		h.translatedSvc.setResponseStore(store)
	}
}

func (h *Handler) translatedInference() *translatedInferenceService {
	h.translatedSvcOnce.Do(func() {
		s := &translatedInferenceService{
			provider:                 h.provider,
			modelResolver:            h.modelResolver,
			modelAuthorizer:          h.modelAuthorizer,
			workflowPolicyResolver:   h.workflowPolicyResolver,
			fallbackResolver:         h.fallbackResolver,
			translatedRequestPatcher: h.translatedRequestPatcher,
			logger:                   h.logger,
			usageLogger:              h.usageLogger,
			budgetChecker:            h.budgetChecker,
			pricingResolver:          h.pricingResolver,
			responseCache:            h.responseCache,
			guardrailsHash:           h.guardrailsHash,
			responseStore:            h.currentResponseStore(),
		}
		s.initHandlers()
		h.responseStoreMu.Lock()
		s.setResponseStore(h.responseStore)
		h.translatedSvc = s
		h.responseStoreMu.Unlock()
	})
	h.responseStoreMu.RLock()
	defer h.responseStoreMu.RUnlock()
	return h.translatedSvc
}

func (h *Handler) nativeBatch() *nativeBatchService {
	return &nativeBatchService{
		provider:                             h.provider,
		modelResolver:                        h.modelResolver,
		modelAuthorizer:                      h.modelAuthorizer,
		workflowPolicyResolver:               h.workflowPolicyResolver,
		batchRequestPreparer:                 h.batchRequestPreparer,
		batchStore:                           h.batchStore,
		cleanupPreparedBatchInputFile:        h.cleanupPreparedBatchInputFile,
		cleanupStoredBatchRewrittenInputFile: h.cleanupStoredBatchRewrittenInputFile,
		usageLogger:                          h.usageLogger,
		budgetChecker:                        h.budgetChecker,
		pricingResolver:                      h.pricingResolver,
	}
}

func (h *Handler) nativeFiles() *nativeFileService {
	return &nativeFileService{provider: h.provider}
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

func (h *Handler) currentResponseStore() responsestore.Store {
	h.responseStoreMu.RLock()
	defer h.responseStoreMu.RUnlock()
	return h.responseStore
}

func (h *Handler) passthrough() *passthroughService {
	return &passthroughService{
		provider:                     h.provider,
		modelAuthorizer:              h.modelAuthorizer,
		logger:                       h.logger,
		usageLogger:                  h.usageLogger,
		budgetChecker:                h.budgetChecker,
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
	return h.passthrough().ProviderPassthrough(c)
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
		if filtered, ok := h.exposedModelLister.(FilteredExposedModelLister); ok && h.modelAuthorizer != nil {
			resp = mergeExposedModelsResponse(resp, filtered.ExposedModelsFiltered(func(selector core.ModelSelector) bool {
				return h.modelAuthorizer.AllowsModel(c.Request().Context(), selector)
			}))
		} else {
			exposed := h.exposedModelLister.ExposedModels()
			if h.modelAuthorizer != nil {
				filtered := make([]core.Model, 0, len(exposed))
				for _, model := range exposed {
					selector, err := core.ParseModelSelector(model.ID, "")
					if err != nil || !h.modelAuthorizer.AllowsModel(c.Request().Context(), selector) {
						continue
					}
					filtered = append(filtered, model)
				}
				exposed = filtered
			}
			resp = mergeExposedModelsResponse(resp, exposed)
		}
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
