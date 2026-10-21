package server

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// dispatchSystemOne forwards a guarded System One body unchanged, apart from
// each target's model name, to the targets that serve the route.
func (s *translatedInferenceService) dispatchSystemOne(c *echo.Context, route systemOneRoute, workflow *core.Workflow, body []byte) error {
	passthroughProvider, ok := s.provider.(core.RoutablePassthrough)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("provider passthrough is not supported by the current provider router", nil))
	}
	eligible := func(selector core.ModelSelector, providerType string) bool {
		return s.systemOneUnsupportedReason(route, selector, providerType) == ""
	}
	return s.dispatchNativeJSON(c, route.nativeJSONRoute, workflow, eligible,
		func(ctx context.Context, selector core.ModelSelector, providerType, providerName string, headers http.Header) (*core.PassthroughResponse, error) {
			return sendSystemOne(ctx, passthroughProvider, route, selector, providerType, providerName, headers, body)
		})
}

// sendSystemOne sends the body to one target under its own model name. Only
// targets that serve the route reach it: the handler checks the primary and
// the failover sweep skips ineligible targets.
func sendSystemOne(
	ctx context.Context,
	passthroughProvider core.RoutablePassthrough,
	route systemOneRoute,
	selector core.ModelSelector,
	providerType, providerName string,
	headers http.Header,
	body []byte,
) (*core.PassthroughResponse, error) {
	forwarded, err := rewriteMessagesModel(body, selector.Model)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	resp, err := passthroughProvider.Passthrough(ctx, providerType, &core.PassthroughRequest{
		Method:       http.MethodPost,
		Endpoint:     route.endpoint,
		Operation:    route.operation,
		Model:        selector.Model,
		Body:         io.NopCloser(bytes.NewReader(forwarded)),
		Headers:      headers,
		ProviderName: providerName,
	})
	return nativeJSONResult(providerType, resp, err)
}
