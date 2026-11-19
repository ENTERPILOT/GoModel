package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// RealtimeTarget resolves the upstream realtime websocket for the model's owning
// provider, requiring it to implement core.RealtimeProvider. It mirrors the audio
// routing: resolve the model, narrow to the capability, and forward the bare
// provider model id.
func (r *Router) RealtimeTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("realtime request is required", nil)
	}
	route, err := r.resolveProvider(ctx, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	p, selector := route.provider, route.selector
	rp, ok := p.(core.RealtimeProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("model %q does not support realtime sessions", req.Model), nil)
	}
	if err := checkRealtimeIntent(p, req.Model, req.Intent); err != nil {
		return nil, err
	}
	target, err := rp.RealtimeTarget(ctx, &core.RealtimeRequest{
		Model:    selector.Model,
		Provider: selector.Provider,
		CallID:   req.CallID,
		Intent:   req.Intent,
	})
	if err != nil || target == nil {
		return target, err
	}
	target.Headers = r.withProviderHeaders(p, target.Headers)
	return target, nil
}

// RealtimeCallTarget resolves the upstream HTTP endpoint for the WebRTC SDP
// exchange, requiring the model's provider to implement core.RealtimeCallProvider.
func (r *Router) RealtimeCallTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return r.realtimeCallTarget(ctx, req, core.RealtimeCallProvider.RealtimeCallTarget)
}

// RealtimeClientSecretTarget resolves the upstream HTTP endpoint for minting
// ephemeral realtime client secrets.
func (r *Router) RealtimeClientSecretTarget(ctx context.Context, req *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error) {
	return r.realtimeCallTarget(ctx, req, core.RealtimeCallProvider.RealtimeClientSecretTarget)
}

// realtimeCallTarget mirrors RealtimeTarget for the realtime HTTP signaling
// endpoints: resolve the model, narrow to the capability, and forward the bare
// provider model id.
func (r *Router) realtimeCallTarget(
	ctx context.Context,
	req *core.RealtimeRequest,
	call func(core.RealtimeCallProvider, context.Context, *core.RealtimeRequest) (*core.RealtimeHTTPTarget, error),
) (*core.RealtimeHTTPTarget, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("realtime request is required", nil)
	}
	route, err := r.resolveProvider(ctx, req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	p, selector := route.provider, route.selector
	rp, ok := p.(core.RealtimeCallProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("model %q does not support realtime calls", req.Model), nil)
	}
	if err := checkRealtimeIntent(p, req.Model, req.Intent); err != nil {
		return nil, err
	}
	target, err := call(rp, ctx, &core.RealtimeRequest{
		Model:    selector.Model,
		Provider: selector.Provider,
		Intent:   req.Intent,
	})
	if err != nil || target == nil {
		return target, err
	}
	target.Headers = r.withProviderHeaders(p, target.Headers)
	return target, nil
}

// withProviderHeaders adds the provider's configured headers to the headers
// of a realtime target. Realtime dials and signaling requests do not go
// through llmclient, which applies them to every other call, so this is the
// one place they join a realtime request. As there, they go last and replace
// a value of the same name. headers is returned as is when the provider has
// none configured.
func (r *Router) withProviderHeaders(p core.Provider, headers http.Header) http.Header {
	if r.caps.headers == nil {
		return headers
	}
	configured := r.caps.headers.ProviderHeaders(p)
	if len(configured) == 0 {
		return headers
	}
	merged := headers.Clone()
	if merged == nil {
		merged = make(http.Header, len(configured))
	}
	for name, value := range configured {
		merged.Set(name, value)
	}
	return merged
}

// checkRealtimeIntent rejects a specialized session intent the resolved provider
// does not serve. Providers build their targets from the intent, so one that
// ignores it would answer a translation request with an ordinary conversation
// session — or mint a client secret for one. Failing here keeps that mismatch
// from reaching the caller as a valid-looking session.
func checkRealtimeIntent(p core.Provider, model, intent string) error {
	if strings.TrimSpace(intent) == "" {
		return nil
	}
	if ip, ok := p.(core.RealtimeIntentProvider); ok && ip.SupportsRealtimeIntent(intent) {
		return nil
	}
	return core.NewInvalidRequestError(fmt.Sprintf("model %q does not support %s realtime sessions", model, strings.TrimSpace(intent)), nil)
}
