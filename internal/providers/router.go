// Package providers provides a router for multiple LLM providers.
package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"gomodel/internal/core"
)

// ErrRegistryNotInitialized is returned when the router is used before the registry has any models.
var ErrRegistryNotInitialized = fmt.Errorf("model registry has no models: ensure Initialize() or LoadFromCache() is called before using the router")

// Router routes requests to the appropriate provider based on the model lookup.
// It uses a dynamic model-to-provider mapping that is populated at startup
// by fetching available models from each provider's /models endpoint.
type Router struct {
	lookup core.ModelLookup
}

type providerTypeRegistry interface {
	ProviderByType(providerType string) core.Provider
}

type providerNameRegistry interface {
	ProviderByName(providerName string) core.Provider
}

type initializedLookup interface {
	IsInitialized() bool
}

type providerTypeLister interface {
	ProviderTypes() []string
}

type providerNameLister interface {
	ProviderNames() []string
}

type publicModelLister interface {
	ListPublicModels() []core.Model
}

type modelWithProviderLister interface {
	ListModelsWithProvider() []ModelWithProvider
}

// qualifiedSelectorResolver is an optional fast path for qualified selector
// resolution. Implementations resolve a "<segment>/<modelID>" pair via an O(1)
// index instead of scanning the catalog. A false result means the caller should
// fall back to the slower catalog scan for raw/edge-case selectors.
type qualifiedSelectorResolver interface {
	ResolveProviderSelector(segment, modelID string) (core.ModelSelector, bool)
}

type providerModelRefresher interface {
	RefreshProviderModels(ctx context.Context, providerSelector string) (int, error)
}

func registryUnavailableError(err error) error {
	return core.NewProviderError("", http.StatusServiceUnavailable, err.Error(), err)
}

// NewRouter creates a new provider router with a model lookup.
// The lookup must be initialized (via Initialize() or LoadFromCache()) before using the router.
// Returns an error if the lookup is nil.
func NewRouter(lookup core.ModelLookup) (*Router, error) {
	if lookup == nil {
		return nil, fmt.Errorf("lookup cannot be nil")
	}
	return &Router{
		lookup: lookup,
	}, nil
}

// checkReady verifies the lookup has models available.
// Returns ErrRegistryNotInitialized if no models are loaded.
func (r *Router) checkReady() error {
	if r.lookup.ModelCount() == 0 {
		return ErrRegistryNotInitialized
	}
	return nil
}

// ResolveModel canonicalizes a requested selector into the concrete
// provider-name-qualified selector used for execution.
//
// Resolution precedence is:
//  1. configured provider name + model ID
//  2. provider type + model ID
//  3. raw slash-shaped model ID (only when provider was not explicit)
//  4. default normalization fallback
func (r *Router) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if err := r.checkReady(); err != nil {
		return core.ModelSelector{}, false, registryUnavailableError(err)
	}

	requested = core.NewRequestedModelSelector(requested.Model, requested.ProviderHint)
	selector, err := requested.Normalize()
	if err != nil {
		return core.ModelSelector{}, false, core.NewInvalidRequestError(err.Error(), err)
	}

	resolved := selector
	if selector.Provider == "" {
		if concrete, ok := r.resolveUnqualifiedSelector(selector); ok {
			resolved = concrete
		}
	} else if concrete, ok := r.resolveQualifiedSelector(requested, selector); ok {
		resolved = concrete
	}

	return resolved, resolved.QualifiedModel() != selector.QualifiedModel(), nil
}

func (r *Router) resolveUnqualifiedSelector(selector core.ModelSelector) (core.ModelSelector, bool) {
	if selector.Provider != "" || strings.TrimSpace(selector.Model) == "" {
		return core.ModelSelector{}, false
	}
	providerName := strings.TrimSpace(r.lookup.GetProviderName(selector.Model))
	if providerName == "" {
		return core.ModelSelector{}, false
	}
	return core.ModelSelector{Provider: providerName, Model: selector.Model}, true
}

func (r *Router) resolveQualifiedSelector(requested core.RequestedModelSelector, selector core.ModelSelector) (core.ModelSelector, bool) {
	models, ok := r.lookup.(modelWithProviderLister)
	if !ok {
		return core.ModelSelector{}, false
	}

	providerSegment := strings.TrimSpace(selector.Provider)
	modelID := strings.TrimSpace(selector.Model)
	if providerSegment == "" || modelID == "" {
		return core.ModelSelector{}, false
	}

	// O(1) fast path: direct provider name/type match. Falls through to the
	// catalog scan only for raw slash-shaped IDs and other edge cases.
	if resolver, ok := r.lookup.(qualifiedSelectorResolver); ok {
		if concrete, ok := resolver.ResolveProviderSelector(providerSegment, modelID); ok {
			return concrete, true
		}
	}

	// Fallback for lookups that don't implement qualifiedSelectorResolver (and for
	// raw slash-shaped model IDs the fast path can't key on). The parsed-modelID
	// pass mirrors the fast path for non-indexed lookups; the requested.Model pass
	// additionally resolves models whose own IDs contain a slash.
	entries := models.ListModelsWithProvider()

	if concrete, ok := resolveProviderOwnedRawSelector(entries, providerSegment, modelID); ok {
		return concrete, true
	}
	if concrete, ok := resolveProviderOwnedRawSelector(entries, providerSegment, requested.Model); ok {
		return concrete, true
	}

	if requested.ExplicitProvider {
		return core.ModelSelector{}, false
	}
	if r.hasConfiguredProviderName(providerSegment) {
		return core.ModelSelector{}, false
	}
	if r.providerByTypeRegistry(providerSegment) != nil {
		return core.ModelSelector{}, false
	}

	rawModelID := strings.TrimSpace(requested.Model)
	if rawModelID == "" {
		return core.ModelSelector{}, false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Model.ID) != rawModelID {
			continue
		}
		return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
	}

	return core.ModelSelector{}, false
}

func resolveProviderOwnedRawSelector(entries []ModelWithProvider, providerSegment, rawModelID string) (core.ModelSelector, bool) {
	providerSegment = strings.TrimSpace(providerSegment)
	rawModelID = strings.TrimSpace(rawModelID)
	if providerSegment == "" || rawModelID == "" {
		return core.ModelSelector{}, false
	}

	for _, entry := range entries {
		if strings.TrimSpace(entry.ProviderName) != providerSegment {
			continue
		}
		if strings.TrimSpace(entry.Model.ID) != rawModelID {
			continue
		}
		return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
	}

	for _, entry := range entries {
		if strings.TrimSpace(entry.ProviderType) != providerSegment {
			continue
		}
		if strings.TrimSpace(entry.Model.ID) != rawModelID {
			continue
		}
		return core.ModelSelector{Provider: entry.ProviderName, Model: entry.Model.ID}, true
	}

	return core.ModelSelector{}, false
}

func (r *Router) hasConfiguredProviderName(providerName string) bool {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return false
	}
	if named, ok := r.lookup.(providerNameLister); ok {
		for _, candidate := range named.ProviderNames() {
			if strings.TrimSpace(candidate) == providerName {
				return true
			}
		}
		return false
	}
	if models, ok := r.lookup.(modelWithProviderLister); ok {
		for _, entry := range models.ListModelsWithProvider() {
			if strings.TrimSpace(entry.ProviderName) == providerName {
				return true
			}
		}
	}
	return false
}

// resolveProvider validates readiness, parses the model selector, and finds the target provider.
func (r *Router) resolveProvider(ctx context.Context, model, providerHint string) (core.Provider, core.ModelSelector, error) {
	requested := core.NewRequestedModelSelector(model, providerHint)
	selector, _, err := r.ResolveModel(requested)
	refreshed := false
	if err != nil {
		var refreshErr error
		refreshed, refreshErr = r.refreshProviderModelsForRequest(ctx, requested)
		if refreshErr != nil {
			return nil, core.ModelSelector{}, refreshErr
		}
		if !refreshed {
			return nil, core.ModelSelector{}, err
		}
		selector, _, err = r.ResolveModel(requested)
		if err != nil {
			return nil, core.ModelSelector{}, err
		}
	}

	lookupModel := selector.QualifiedModel()
	p := r.lookup.GetProvider(lookupModel)
	if p == nil && !refreshed {
		var refreshErr error
		refreshed, refreshErr = r.refreshProviderModelsForRequest(ctx, requested)
		if refreshErr != nil {
			return nil, core.ModelSelector{}, refreshErr
		}
		if refreshed {
			selector, _, err = r.ResolveModel(requested)
			if err != nil {
				return nil, core.ModelSelector{}, err
			}
			lookupModel = selector.QualifiedModel()
			p = r.lookup.GetProvider(lookupModel)
		}
	}
	if p == nil {
		return nil, core.ModelSelector{}, core.NewNotFoundError("model not found: " + lookupModel)
	}
	return p, selector, nil
}

func (r *Router) refreshProviderModelsForRequest(ctx context.Context, requested core.RequestedModelSelector) (bool, error) {
	refresher, ok := r.lookup.(providerModelRefresher)
	if !ok {
		return false, nil
	}

	selector, err := requested.Normalize()
	if err != nil {
		return false, nil
	}
	providerSelector := strings.TrimSpace(selector.Provider)
	if providerSelector == "" {
		return false, nil
	}
	if !r.hasRegisteredProviderSelector(providerSelector) {
		return false, nil
	}

	_, err = refresher.RefreshProviderModels(ctx, providerSelector)
	return true, err
}

// RefreshProviderModels refreshes a configured provider's model inventory when
// the backing lookup supports request-time provider refreshes.
func (r *Router) RefreshProviderModels(ctx context.Context, providerSelector string) (int, error) {
	providerSelector = strings.TrimSpace(providerSelector)
	if !r.hasRegisteredProviderSelector(providerSelector) {
		return 0, nil
	}
	refresher, ok := r.lookup.(providerModelRefresher)
	if !ok {
		return 0, nil
	}
	return refresher.RefreshProviderModels(ctx, providerSelector)
}

func (r *Router) hasRegisteredProviderSelector(providerSelector string) bool {
	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return false
	}
	if r.hasConfiguredProviderName(providerSelector) {
		return true
	}
	if strings.TrimSpace(r.lookup.GetProviderNameForType(providerSelector)) != "" {
		return true
	}
	return r.providerByTypeRegistry(providerSelector) != nil
}

func (r *Router) resolveProviderType(providerType string) (core.Provider, error) {
	if err := r.ensureProviderInventoryReady(); err != nil {
		return nil, err
	}
	if providerType == "" {
		return nil, core.NewInvalidRequestError("provider type is required", nil)
	}
	provider := r.providerByTypeRegistry(providerType)
	if provider == nil {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider type: %s", providerType), nil)
	}
	return provider, nil
}

func (r *Router) resolveProviderSelector(providerSelector string) (core.Provider, string, error) {
	if err := r.ensureProviderInventoryReady(); err != nil {
		return nil, "", err
	}
	providerSelector = strings.TrimSpace(providerSelector)
	if providerSelector == "" {
		return nil, "", core.NewInvalidRequestError("provider is required", nil)
	}
	if provider := r.providerByTypeRegistry(providerSelector); provider != nil {
		return provider, providerSelector, nil
	}
	if provider := r.providerByNameRegistry(providerSelector); provider != nil {
		providerType := strings.TrimSpace(r.GetProviderTypeForName(providerSelector))
		if providerType == "" {
			providerType = providerSelector
		}
		return provider, providerType, nil
	}
	return nil, "", core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider: %s", providerSelector), nil)
}

func (r *Router) ensureProviderInventoryReady() error {
	if initialized, ok := r.lookup.(initializedLookup); ok {
		if !initialized.IsInitialized() {
			if err := r.checkReady(); err != nil {
				if errors.Is(err, ErrRegistryNotInitialized) {
					return registryUnavailableError(err)
				}
				return err
			}
		}
	} else if err := r.checkReady(); err != nil {
		if errors.Is(err, ErrRegistryNotInitialized) {
			return registryUnavailableError(err)
		}
		return err
	}
	return nil
}

// assertProviderCapability narrows a resolved provider to capability T. It
// propagates any resolution error unchanged and, when the provider does not
// implement T, returns the error built by unsupported.
func assertProviderCapability[T any](provider core.Provider, err error, unsupported func() error) (T, error) {
	var capability T
	if err != nil {
		return capability, err
	}
	capability, ok := provider.(T)
	if !ok {
		return capability, unsupported()
	}
	return capability, nil
}

func (r *Router) resolveNativeBatchProvider(providerType string) (core.NativeBatchProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	return assertProviderCapability[core.NativeBatchProvider](provider, err, func() error {
		return core.NewInvalidRequestError(fmt.Sprintf("%s does not support native batch processing", providerType), nil)
	})
}

func (r *Router) resolveNativeFileProvider(providerType string) (core.NativeFileProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	return assertProviderCapability[core.NativeFileProvider](provider, err, func() error {
		return core.NewInvalidRequestError(fmt.Sprintf("%s does not support native file operations", providerType), nil)
	})
}

func (r *Router) resolveNativeResponseLifecycleProvider(providerType string) (core.NativeResponseLifecycleProvider, string, error) {
	provider, resolvedProviderType, err := r.resolveProviderSelector(providerType)
	rp, err := assertProviderCapability[core.NativeResponseLifecycleProvider](provider, err, func() error {
		return unsupportedNativeResponseOperation(fmt.Sprintf("%s does not support native response lifecycle operations", providerType))
	})
	return rp, resolvedProviderType, err
}

func (r *Router) resolveNativeResponseUtilityProvider(providerType string) (core.NativeResponseUtilityProvider, string, error) {
	provider, resolvedProviderType, err := r.resolveProviderSelector(providerType)
	rp, err := assertProviderCapability[core.NativeResponseUtilityProvider](provider, err, func() error {
		return unsupportedNativeResponseOperation(fmt.Sprintf("%s does not support native response utility operations", providerType))
	})
	return rp, resolvedProviderType, err
}

func unsupportedNativeResponseOperation(message string) *core.GatewayError {
	return core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, message, nil).WithCode("unsupported_response_operation")
}

func (r *Router) resolvePassthroughProvider(providerType string) (core.PassthroughProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	return assertProviderCapability[core.PassthroughProvider](provider, err, func() error {
		return core.NewInvalidRequestError(fmt.Sprintf("%s does not support provider passthrough", providerType), nil)
	})
}

func routeResolvedModelCall[Req any, Resp any](
	r *Router,
	ctx context.Context,
	model string,
	providerHint string,
	buildForward func(core.ModelSelector) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, string, error) {
	p, selector, err := r.resolveProvider(ctx, model, providerHint)
	if err != nil {
		var zero Resp
		return zero, "", err
	}

	resp, err := call(ctx, p, buildForward(selector))
	return resp, r.GetProviderType(selector.QualifiedModel()), err
}

func routeStampedModelResponse[Req any, Resp any](
	r *Router,
	ctx context.Context,
	model string,
	providerHint string,
	buildForward func(core.ModelSelector) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, error) {
	resp, providerType, err := routeResolvedModelCall(r, ctx, model, providerHint, buildForward, call)
	if err != nil {
		var zero Resp
		return zero, err
	}
	return stampProvider(resp, providerType), nil
}

func routeNativeBatchCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeBatchProvider) (T, error)) (T, error) {
	bp, err := r.resolveNativeBatchProvider(providerType)
	if err != nil {
		var zero T
		return zero, err
	}
	return call(ctx, bp)
}

func routeNativeFileCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeFileProvider) (T, error)) (T, error) {
	fp, err := r.resolveNativeFileProvider(providerType)
	if err != nil {
		var zero T
		return zero, err
	}
	return call(ctx, fp)
}

func routeNativeResponseLifecycleCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeResponseLifecycleProvider) (T, error)) (T, string, error) {
	rp, resolvedProviderType, err := r.resolveNativeResponseLifecycleProvider(providerType)
	if err != nil {
		var zero T
		return zero, "", err
	}
	resp, err := call(ctx, rp)
	return resp, resolvedProviderType, err
}

func routeNativeResponseUtilityCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeResponseUtilityProvider) (T, error)) (T, string, error) {
	rp, resolvedProviderType, err := r.resolveNativeResponseUtilityProvider(providerType)
	if err != nil {
		var zero T
		return zero, "", err
	}
	resp, err := call(ctx, rp)
	return resp, resolvedProviderType, err
}

func stampProvider[T any](resp T, providerType string) T {
	switch typed := any(resp).(type) {
	case *core.ChatResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.ResponsesResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.EmbeddingResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.BatchResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.FileObject:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.ResponseCompactResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	}
	return resp
}

// Provider is gateway routing metadata on OpenAI-compatible request structs and
// must be removed before dispatching to an upstream provider implementation.
func forwardChatRequest(req *core.ChatRequest, selector core.ModelSelector) *core.ChatRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardResponsesRequest(req *core.ResponsesRequest, selector core.ModelSelector) *core.ResponsesRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardEmbeddingRequest(req *core.EmbeddingRequest, selector core.ModelSelector) *core.EmbeddingRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardAudioSpeechRequest(req *core.AudioSpeechRequest, selector core.ModelSelector) *core.AudioSpeechRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardAudioTranscriptionRequest(req *core.AudioTranscriptionRequest, selector core.ModelSelector) *core.AudioTranscriptionRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func callChatCompletion(ctx context.Context, provider core.Provider, req *core.ChatRequest) (*core.ChatResponse, error) {
	return provider.ChatCompletion(ctx, req)
}

func callResponses(ctx context.Context, provider core.Provider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return provider.Responses(ctx, req)
}

func callEmbeddings(ctx context.Context, provider core.Provider, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return provider.Embeddings(ctx, req)
}

// Supports returns true if any provider supports the given model.
// Returns false if the lookup has no models loaded.
func (r *Router) Supports(model string) bool {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return false
	}
	return r.lookup.Supports(selector.QualifiedModel())
}

// ModelCount returns the number of models currently loaded into the router lookup.
func (r *Router) ModelCount() int {
	if r == nil || r.lookup == nil {
		return 0
	}
	return r.lookup.ModelCount()
}

// ChatCompletion routes the request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ChatRequest {
			return forwardChatRequest(req, selector)
		},
		callChatCompletion,
	)
}

// StreamChatCompletion routes the streaming request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	stream, _, err := routeResolvedModelCall(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ChatRequest {
			return forwardChatRequest(req, selector)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ChatRequest) (io.ReadCloser, error) {
			return provider.StreamChatCompletion(ctx, forwardReq)
		},
	)
	return stream, err
}

// ListModels returns all models from the lookup.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if err := r.checkReady(); err != nil {
		return nil, registryUnavailableError(err)
	}
	var models []core.Model
	if public, ok := r.lookup.(publicModelLister); ok {
		models = public.ListPublicModels()
	} else {
		models = r.lookup.ListModels()
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   models,
	}, nil
}

// Responses routes the Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ResponsesRequest {
			return forwardResponsesRequest(req, selector)
		},
		callResponses,
	)
}

// StreamResponses routes the streaming Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	stream, _, err := routeResolvedModelCall(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ResponsesRequest {
			return forwardResponsesRequest(req, selector)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ResponsesRequest) (io.ReadCloser, error) {
			return provider.StreamResponses(ctx, forwardReq)
		},
	)
	return stream, err
}

// Embeddings routes the embeddings request to the appropriate provider.
func (r *Router) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	resp, err := routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.EmbeddingRequest {
			return forwardEmbeddingRequest(req, selector)
		},
		callEmbeddings,
	)
	if err != nil {
		return nil, err
	}
	// Some OpenAI-compatible servers ignore encoding_format and always return
	// float arrays; re-encode to the format the client asked for so SDKs that
	// default to base64 (OpenAI, LangChain) don't mis-decode the response.
	core.NormalizeEmbeddingEncoding(resp, req.EncodingFormat)
	return resp, nil
}

// CreateSpeech routes a text-to-speech request to the provider that owns the model.
func (r *Router) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return routeAudioCall(
		r, ctx, req.Model, req.Provider,
		func(selector core.ModelSelector) *core.AudioSpeechRequest {
			return forwardAudioSpeechRequest(req, selector)
		},
		func(ctx context.Context, ap core.AudioProvider, forwardReq *core.AudioSpeechRequest) (*core.AudioResponse, error) {
			return ap.CreateSpeech(ctx, forwardReq)
		},
	)
}

// CreateTranscription routes a speech-to-text request to the provider that owns the model.
func (r *Router) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return routeAudioCall(
		r, ctx, req.Model, req.Provider,
		func(selector core.ModelSelector) *core.AudioTranscriptionRequest {
			return forwardAudioTranscriptionRequest(req, selector)
		},
		func(ctx context.Context, ap core.AudioProvider, forwardReq *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
			return ap.CreateTranscription(ctx, forwardReq)
		},
	)
}

// routeAudioCall resolves the model, requires the target provider to implement
// core.AudioProvider, and invokes call. It mirrors routeNative*Call but for the
// optional audio capability.
func routeAudioCall[Req any](
	r *Router,
	ctx context.Context,
	model, providerHint string,
	forward func(core.ModelSelector) Req,
	call func(context.Context, core.AudioProvider, Req) (*core.AudioResponse, error),
) (*core.AudioResponse, error) {
	resp, _, err := routeResolvedModelCall(
		r, ctx, model, providerHint, forward,
		func(ctx context.Context, provider core.Provider, forwardReq Req) (*core.AudioResponse, error) {
			ap, ok := provider.(core.AudioProvider)
			if !ok {
				return nil, audioUnsupportedError(model)
			}
			return call(ctx, ap, forwardReq)
		},
	)
	return resp, err
}

func audioUnsupportedError(model string) error {
	return core.NewInvalidRequestError(fmt.Sprintf("model %q does not support audio operations", model), nil)
}

// RealtimeTarget resolves the upstream realtime websocket for the model's owning
// provider, requiring it to implement core.RealtimeProvider. It mirrors the audio
// routing: resolve the model, narrow to the capability, and forward the bare
// provider model id.
func (r *Router) RealtimeTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("realtime request is required", nil)
	}
	p, selector, err := r.resolveProvider(ctx, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	rp, ok := p.(core.RealtimeProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("model %q does not support realtime sessions", req.Model), nil)
	}
	return rp.RealtimeTarget(ctx, &core.RealtimeRequest{
		Model:    selector.Model,
		Provider: selector.Provider,
	})
}

// GetProviderType returns the provider type string for the given model.
// Returns empty string if the model is not found.
func (r *Router) GetProviderType(model string) string {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return ""
	}
	return r.lookup.GetProviderType(selector.QualifiedModel())
}

// GetProviderName returns the concrete configured provider instance name for
// the given model selector. Returns empty string when unavailable.
func (r *Router) GetProviderName(model string) string {
	selector, _, err := r.ResolveModel(core.NewRequestedModelSelector(model, ""))
	if err != nil {
		return ""
	}
	if !r.lookup.Supports(selector.QualifiedModel()) {
		return ""
	}
	if selector.Provider != "" {
		return selector.Provider
	}
	return r.lookup.GetProviderName(selector.QualifiedModel())
}

// GetProviderNameForType returns the concrete configured provider instance name
// chosen for a provider-typed route.
func (r *Router) GetProviderNameForType(providerType string) string {
	return strings.TrimSpace(r.lookup.GetProviderNameForType(providerType))
}

// GetProviderTypeForName returns the provider type for a concrete configured
// provider instance name.
func (r *Router) GetProviderTypeForName(providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	return strings.TrimSpace(r.lookup.GetProviderTypeForName(providerName))
}

func (r *Router) providerByType(providerType string) core.Provider {
	models := r.lookup.ListModels()
	for _, model := range models {
		if r.lookup.GetProviderType(model.ID) != providerType {
			continue
		}
		p := r.lookup.GetProvider(model.ID)
		if p != nil {
			return p
		}
	}
	return nil
}

func (r *Router) providerByTypeRegistry(providerType string) core.Provider {
	if registry, ok := r.lookup.(providerTypeRegistry); ok {
		if provider := registry.ProviderByType(providerType); provider != nil {
			return provider
		}
	}
	return r.providerByType(providerType)
}

func (r *Router) providerByNameRegistry(providerName string) core.Provider {
	if registry, ok := r.lookup.(providerNameRegistry); ok {
		if provider := registry.ProviderByName(providerName); provider != nil {
			return provider
		}
	}
	return r.providerByName(providerName)
}

func (r *Router) providerByName(providerName string) core.Provider {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil
	}
	models, ok := r.lookup.(modelWithProviderLister)
	if !ok {
		return nil
	}
	for _, entry := range models.ListModelsWithProvider() {
		if strings.TrimSpace(entry.ProviderName) != providerName {
			continue
		}
		modelID := strings.TrimSpace(entry.Model.ID)
		if modelID == "" {
			continue
		}
		if provider := r.lookup.GetProvider(core.ModelSelector{Provider: providerName, Model: modelID}.QualifiedModel()); provider != nil {
			return provider
		}
	}
	return nil
}

func (r *Router) providerTypes() []string {
	if typed, ok := r.lookup.(providerTypeLister); ok {
		return typed.ProviderTypes()
	}

	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, model := range r.lookup.ListModels() {
		providerType := strings.TrimSpace(r.lookup.GetProviderType(model.ID))
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		result = append(result, providerType)
	}
	sort.Strings(result)
	return result
}

// providerTypesSupporting returns the registered provider types whose backing
// provider satisfies the given capability check. The inventory is independent of
// the public model catalog whenever the underlying lookup can expose provider
// types directly.
func (r *Router) providerTypesSupporting(supports func(core.Provider) bool) []string {
	providerTypes := r.providerTypes()
	result := make([]string, 0, len(providerTypes))
	for _, providerType := range providerTypes {
		provider := r.providerByTypeRegistry(providerType)
		if provider != nil && supports(provider) {
			result = append(result, providerType)
		}
	}
	return result
}

// NativeFileProviderTypes returns the registered provider types that support
// native file operations.
func (r *Router) NativeFileProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeFileProvider)
		return ok
	})
}

// NativeBatchProviderTypes returns the registered provider types that support
// native batch operations.
func (r *Router) NativeBatchProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeBatchProvider)
		return ok
	})
}

// NativeResponseProviderTypes returns the registered provider types that
// support native Responses lifecycle operations.
func (r *Router) NativeResponseProviderTypes() []string {
	return r.providerTypesSupporting(func(p core.Provider) bool {
		_, ok := p.(core.NativeResponseLifecycleProvider)
		return ok
	})
}

// Passthrough routes an opaque provider-native request by provider type.
// If req.ProviderName is set, routing prefers the named provider instance over
// the first registered provider of the given type.
func (r *Router) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	var pp core.PassthroughProvider
	if req != nil && strings.TrimSpace(req.ProviderName) != "" {
		slog.DebugContext(ctx, "passthrough routing by name", "providerName", req.ProviderName, "providerType", providerType)
		if p := r.providerByNameRegistry(strings.TrimSpace(req.ProviderName)); p != nil {
			if named, ok := p.(core.PassthroughProvider); ok {
				pp = named
				slog.DebugContext(ctx, "passthrough routed by name", "providerName", req.ProviderName)
			} else {
				slog.DebugContext(ctx, "passthrough provider found by name but does not implement PassthroughProvider", "providerName", req.ProviderName)
			}
		} else {
			slog.DebugContext(ctx, "passthrough provider not found by name, falling back to type", "providerName", req.ProviderName, "providerType", providerType)
		}
	}
	if pp == nil {
		var err error
		pp, err = r.resolvePassthroughProvider(providerType)
		if err != nil {
			return nil, err
		}
	}
	return pp.Passthrough(ctx, req)
}

// CreateBatch routes native batch creation to a provider type.
func (r *Router) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.CreateBatch(ctx, req)
	})
	return stampProvider(resp, providerType), err
}

// CreateBatchWithHints routes native batch creation and returns any provider
// batch-result shaping hints that need gateway persistence.
func (r *Router) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	type createBatchWithHintsResult struct {
		resp  *core.BatchResponse
		hints map[string]string
	}
	result, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (createBatchWithHintsResult, error) {
		if hinted, ok := bp.(core.BatchCreateHintAwareProvider); ok {
			resp, hints, err := hinted.CreateBatchWithHints(ctx, req)
			return createBatchWithHintsResult{resp: resp, hints: hints}, err
		}
		resp, err := bp.CreateBatch(ctx, req)
		return createBatchWithHintsResult{resp: resp}, err
	})
	return stampProvider(result.resp, providerType), result.hints, err
}

// GetBatch routes native batch lookup to a provider type.
func (r *Router) GetBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.GetBatch(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// ListBatches routes native batch listing to a provider type.
func (r *Router) ListBatches(ctx context.Context, providerType string, limit int, after string) (*core.BatchListResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchListResponse, error) {
		return bp.ListBatches(ctx, limit, after)
	})
	if err != nil {
		return nil, err
	}
	if resp != nil {
		for i := range resp.Data {
			resp.Data[i].Provider = providerType
		}
	}
	return resp, nil
}

// CancelBatch routes native batch cancellation to a provider type.
func (r *Router) CancelBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.CancelBatch(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// GetBatchResults routes native batch results lookup to a provider type.
func (r *Router) GetBatchResults(ctx context.Context, providerType, id string) (*core.BatchResultsResponse, error) {
	return routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResultsResponse, error) {
		return bp.GetBatchResults(ctx, id)
	})
}

// GetBatchResultsWithHints routes native batch results lookup with persisted
// per-item endpoint hints when the provider supports them.
func (r *Router) GetBatchResultsWithHints(ctx context.Context, providerType, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	return routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResultsResponse, error) {
		if hinted, ok := bp.(core.BatchResultHintAwareProvider); ok && len(endpointByCustomID) > 0 {
			return hinted.GetBatchResultsWithHints(ctx, id, endpointByCustomID)
		}
		return bp.GetBatchResults(ctx, id)
	})
}

// ClearBatchResultHints clears transient provider-side batch result hints once
// they have been persisted by the gateway.
func (r *Router) ClearBatchResultHints(providerType, batchID string) {
	if strings.TrimSpace(batchID) == "" {
		return
	}
	bp, err := r.resolveNativeBatchProvider(providerType)
	if err != nil {
		return
	}
	hinted, ok := bp.(core.BatchResultHintAwareProvider)
	if !ok {
		return
	}
	hinted.ClearBatchResultHints(batchID)
}

// CreateFile routes file upload to a provider type.
func (r *Router) CreateFile(ctx context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileObject, error) {
		return fp.CreateFile(ctx, req)
	})
	return stampProvider(resp, providerType), err
}

// ListFiles routes file listing to a provider type.
func (r *Router) ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileListResponse, error) {
		return fp.ListFiles(ctx, purpose, limit, after)
	})
	if err != nil {
		return nil, err
	}
	if resp != nil {
		for i := range resp.Data {
			resp.Data[i].Provider = providerType
		}
	}
	return resp, nil
}

// GetFile routes file retrieval to a provider type.
func (r *Router) GetFile(ctx context.Context, providerType, id string) (*core.FileObject, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileObject, error) {
		return fp.GetFile(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// DeleteFile routes file deletion to a provider type.
func (r *Router) DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	return routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileDeleteResponse, error) {
		return fp.DeleteFile(ctx, id)
	})
}

// GetFileContent routes file content retrieval to a provider type.
func (r *Router) GetFileContent(ctx context.Context, providerType, id string) (*core.FileContentResponse, error) {
	return routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileContentResponse, error) {
		return fp.GetFileContent(ctx, id)
	})
}

// GetResponse routes native response retrieval to a provider type.
func (r *Router) GetResponse(ctx context.Context, providerType, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponsesResponse, error) {
		return rp.GetResponse(ctx, id, params)
	})
	return stampProvider(resp, resolvedProviderType), err
}

// ListResponseInputItems routes native response input item listing to a provider type.
func (r *Router) ListResponseInputItems(ctx context.Context, providerType, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	resp, _, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponseInputItemListResponse, error) {
		return rp.ListResponseInputItems(ctx, id, params)
	})
	return resp, err
}

// CancelResponse routes native response cancellation to a provider type.
func (r *Router) CancelResponse(ctx context.Context, providerType, id string) (*core.ResponsesResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponsesResponse, error) {
		return rp.CancelResponse(ctx, id)
	})
	return stampProvider(resp, resolvedProviderType), err
}

// DeleteResponse routes native response deletion to a provider type.
func (r *Router) DeleteResponse(ctx context.Context, providerType, id string) (*core.ResponseDeleteResponse, error) {
	resp, _, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponseDeleteResponse, error) {
		return rp.DeleteResponse(ctx, id)
	})
	return resp, err
}

// CountResponseInputTokens routes native response input token counting to a provider type.
func (r *Router) CountResponseInputTokens(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	resp, _, err := routeNativeResponseUtilityCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseUtilityProvider) (*core.ResponseInputTokensResponse, error) {
		return rp.CountResponseInputTokens(ctx, forwardNativeResponseUtilityRequest(req))
	})
	return resp, err
}

// CompactResponse routes native response compaction to a provider type.
func (r *Router) CompactResponse(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseUtilityCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseUtilityProvider) (*core.ResponseCompactResponse, error) {
		return rp.CompactResponse(ctx, forwardNativeResponseUtilityRequest(req))
	})
	return stampProvider(resp, resolvedProviderType), err
}

func forwardNativeResponseUtilityRequest(req *core.ResponsesRequest) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	forwardReq := *req
	forwardReq.Provider = ""
	return &forwardReq
}
