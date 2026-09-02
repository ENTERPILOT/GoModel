// Package server provides HTTP handlers and server setup for the LLM gateway.
package server

import (
	"net/http"
	"sync"

	"github.com/enterpilot/gomodel/internal/auditlog"
	batchstore "github.com/enterpilot/gomodel/internal/batch"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/filestore"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
	"github.com/enterpilot/gomodel/internal/realtime"
	"github.com/enterpilot/gomodel/internal/responsecache"
	"github.com/enterpilot/gomodel/internal/responsestore"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// Handler holds the HTTP handlers
type Handler struct {
	provider                        core.RoutableProvider
	modelResolver                   RequestModelResolver
	modelAuthorizer                 RequestModelAuthorizer
	failoverResolver                RequestFailoverResolver
	failoverPolicy                  *gateway.FailoverPolicy
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
	versionChecker               *versioncheck.Checker

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
			failoverPolicy:           h.failoverPolicy,
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

func (h *Handler) modelCalls() modelCallService {
	return modelCallService{
		provider:        h.provider,
		modelResolver:   h.modelResolver,
		modelAuthorizer: h.modelAuthorizer,
		budgetChecker:   h.budgetChecker,
		rateLimiter:     h.rateLimiter,
		usageLogger:     h.usageLogger,
		pricingResolver: h.pricingResolver,
	}
}

func (h *Handler) audio() *audioService {
	var logBodies, logAudioBodies bool
	if h.logger != nil {
		cfg := h.logger.Config()
		logBodies = cfg.LogBodies
		logAudioBodies = cfg.LogAudioBodies
	}
	return &audioService{
		modelCallService: h.modelCalls(),
		logBodies:        logBodies,
		logAudioBodies:   logAudioBodies,
	}
}

func (h *Handler) images() *imageService {
	svc := &imageService{modelCallService: h.modelCalls()}
	if h.logger != nil {
		cfg := h.logger.Config()
		svc.logBodies = cfg.LogBodies
		svc.logImageInputs = cfg.LogImageInputs
		svc.logImageOutputs = cfg.LogImageOutputs
	}
	return svc
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

// drainSnapshotWrites stops new background response snapshot writes and waits
// for in-flight ones. Called during server shutdown before the response store
// closes. It creates the (lazily initialized) inference service if needed so
// the drain gate is set even when a request races shutdown into first use.
func (h *Handler) drainSnapshotWrites() {
	h.translatedInference().drainSnapshotWrites()
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
