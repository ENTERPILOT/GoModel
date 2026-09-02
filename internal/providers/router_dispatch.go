package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

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
	buildForward func(resolvedRoute) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, string, error) {
	route, err := r.resolveProvider(ctx, model, providerHint)
	if err != nil {
		var zero Resp
		return zero, "", err
	}

	resp, err := call(ctx, route.provider, buildForward(route))
	return resp, route.providerType, err
}

func routeStampedModelResponse[Req any, Resp any](
	r *Router,
	ctx context.Context,
	model string,
	providerHint string,
	buildForward func(resolvedRoute) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, error) {
	resp, providerType, err := routeResolvedModelCall(r, ctx, model, providerHint, buildForward, call)
	if err != nil {
		var zero Resp
		return zero, err
	}
	return stampProvider(resp, providerType), nil
}

func routeModelStream[Req any](
	r *Router,
	ctx context.Context,
	model, providerHint string,
	buildForward func(resolvedRoute) Req,
	call func(context.Context, core.Provider, Req) (io.ReadCloser, error),
) (io.ReadCloser, error) {
	stream, _, err := routeResolvedModelCall(r, ctx, model, providerHint, buildForward, call)
	return stream, err
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
	case *core.ImageGenerationResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	}
	return resp
}
