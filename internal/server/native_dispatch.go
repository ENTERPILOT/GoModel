package server

import (
	"context"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
)

// nativeForwardingProvider returns the provider router's passthrough surface
// when native dialect forwarding is possible for the workflow at all: no
// translated-request patcher is installed and no failover targets are
// configured (both operate on the canonical translated request). Dialect
// gates layer their endpoint-specific conditions on top of this.
func (s *translatedInferenceService) nativeForwardingProvider(workflow *core.Workflow) (core.RoutablePassthrough, bool) {
	if workflow == nil {
		return nil, false
	}
	if s.translatedRequestPatcher != nil {
		return nil, false
	}
	if len(s.inference().FailoverSelectors(workflow)) > 0 {
		return nil, false
	}
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	return passthroughProvider, ok
}

// forwardNative executes a prepared provider-native request through the
// passthrough surface and relays the provider response (JSON or SSE)
// unchanged, with audit and usage stream observers plus any extraObservers.
// req.ProviderName and info.Provider must already be set; info.Provider names
// the resolved provider type for routing and error attribution.
func (s *translatedInferenceService) forwardNative(
	c *echo.Context,
	ctx context.Context,
	req *core.PassthroughRequest,
	info *core.PassthroughRouteInfo,
	extraObservers ...streaming.Observer,
) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}

	resp, err := passthroughProvider.Passthrough(ctx, info.Provider, req)
	if err != nil {
		return handleError(c, err)
	}
	return proxyPassthroughResponse(c, s.logger, s.usageLogger, s.pricingResolver, info.Provider, req.ProviderName, req.Endpoint, info, resp, extraObservers...)
}
