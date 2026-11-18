package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func forwardAudioSpeechRequest(req *core.AudioSpeechRequest, selector core.ModelSelector) *core.AudioSpeechRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardImageGenerationRequest(req *core.ImageGenerationRequest, selector core.ModelSelector) *core.ImageGenerationRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardImageEditRequest(req *core.ImageEditRequest, selector core.ModelSelector) *core.ImageEditRequest {
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

// CreateSpeech routes a text-to-speech request to the provider that owns the model.
func (r *Router) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return routeAudioCall(
		r, ctx, req.Model, req.Provider,
		func(route resolvedRoute) *core.AudioSpeechRequest {
			return forwardAudioSpeechRequest(req, route.selector)
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
		func(route resolvedRoute) *core.AudioTranscriptionRequest {
			return forwardAudioTranscriptionRequest(req, route.selector)
		},
		func(ctx context.Context, ap core.AudioProvider, forwardReq *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
			return ap.CreateTranscription(ctx, forwardReq)
		},
	)
}

// CreateTranslation routes a speech translation request to a provider that
// explicitly supports the OpenAI-compatible translations endpoint.
func (r *Router) CreateTranslation(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio translation request is required", nil)
	}
	resp, _, err := routeResolvedModelCall(
		r, ctx, req.Model, req.Provider,
		func(route resolvedRoute) *core.AudioTranscriptionRequest {
			return forwardAudioTranscriptionRequest(req, route.selector)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
			translator, ok := provider.(core.AudioTranslationProvider)
			if !ok {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("model %q does not support audio translations", req.Model), nil)
			}
			return translator.CreateTranslation(ctx, forwardReq)
		},
	)
	return resp, err
}

// ListVoices routes a voice listing request to the provider that owns the
// model, requiring it to implement core.AudioVoicesProvider. Like model
// discovery it bypasses the circuit breaker: a failing listing endpoint says
// nothing about inference and must not pause speech or chat.
func (r *Router) ListVoices(ctx context.Context, req *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio voices request is required", nil)
	}
	resp, _, err := routeResolvedModelCall(
		r, ctx, req.Model, req.Provider,
		func(route resolvedRoute) *core.AudioVoicesRequest {
			return &core.AudioVoicesRequest{Model: route.selector.Model}
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.AudioVoicesRequest) (*core.AudioVoicesResponse, error) {
			lister, ok := provider.(core.AudioVoicesProvider)
			if !ok {
				return nil, core.NewAudioVoicesUnsupportedError(req.Model)
			}
			return lister.ListVoices(llmclient.WithoutCircuitBreaker(ctx), forwardReq)
		},
	)
	return resp, err
}

// ListCustomVoices routes OpenAI's own voice list to the provider instance
// named providerName. Like ListVoices it is a control-plane read and runs
// outside the provider's circuit breaker.
func (r *Router) ListCustomVoices(ctx context.Context, providerName string, params core.AudioCustomVoicesParams) (*core.AudioCustomVoicesResponse, error) {
	if err := r.ensureProviderInventoryReady(); err != nil {
		return nil, err
	}
	providerName = strings.TrimSpace(providerName)
	lister, ok := r.providerByNameRegistry(providerName).(core.AudioCustomVoicesProvider)
	if !ok || !lister.ServesCustomVoices() {
		return nil, core.NewAudioCustomVoicesUnsupportedError(providerName)
	}
	return lister.ListCustomVoices(llmclient.WithoutCircuitBreaker(ctx), params)
}

// AudioCustomVoiceProviders returns the configured provider instances that
// serve OpenAI's own voice list, in registration order. It asks each instance,
// not its type: a self-hosted server registered as type openai does not.
func (r *Router) AudioCustomVoiceProviders() []string {
	if r.caps.nameLister == nil {
		return nil
	}
	var names []string
	for _, name := range r.caps.nameLister.ProviderNames() {
		if lister, ok := r.providerByNameRegistry(name).(core.AudioCustomVoicesProvider); ok && lister.ServesCustomVoices() {
			names = append(names, name)
		}
	}
	return names
}

// CreateImage routes an image generation request to the provider that owns the
// model, requiring it to implement core.ImageProvider.
func (r *Router) CreateImage(ctx context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("image generation request is required", nil)
	}
	return routeImageCall(
		r, ctx, req.Model, req.Provider, "image generation",
		func(route resolvedRoute) *core.ImageGenerationRequest {
			return forwardImageGenerationRequest(req, route.selector)
		},
		core.ImageProvider.CreateImage,
	)
}

// CreateImageEdit routes an image edit request to the provider that owns the
// model, requiring it to implement core.ImageEditProvider.
func (r *Router) CreateImageEdit(ctx context.Context, req *core.ImageEditRequest) (*core.ImageGenerationResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("image edit request is required", nil)
	}
	return routeImageCall(
		r, ctx, req.Model, req.Provider, "image edits",
		func(route resolvedRoute) *core.ImageEditRequest {
			return forwardImageEditRequest(req, route.selector)
		},
		core.ImageEditProvider.CreateImageEdit,
	)
}

// routeImageCall resolves the model, requires the target provider to implement
// the image capability P, and invokes call, stamping the provider on the
// response. capability names the operation in the unsupported-provider error.
func routeImageCall[Req any, P any](
	r *Router,
	ctx context.Context,
	model, providerHint, capability string,
	forward func(resolvedRoute) Req,
	call func(P, context.Context, Req) (*core.ImageGenerationResponse, error),
) (*core.ImageGenerationResponse, error) {
	return routeStampedModelResponse(
		r, ctx, model, providerHint, forward,
		func(ctx context.Context, provider core.Provider, forwardReq Req) (*core.ImageGenerationResponse, error) {
			p, ok := provider.(P)
			if !ok {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("model %q does not support %s", model, capability), nil)
			}
			return call(p, ctx, forwardReq)
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
	forward func(resolvedRoute) Req,
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
