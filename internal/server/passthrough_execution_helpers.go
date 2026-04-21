package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func finalizePassthroughEndpoint(endpoint string, rawQuery string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", core.NewInvalidRequestError("provider passthrough endpoint is required", nil)
	}
	if q := strings.TrimSpace(rawQuery); q != "" {
		endpoint += "?" + q
	}
	return endpoint, nil
}

// extractPassthroughEndpoint derives the provider-relative endpoint from the
// request URL path, normalizing an optional v1/ prefix when allowed. The
// returned value includes a query string suffix when present.
func extractPassthroughEndpoint(c *echo.Context, allowV1Alias bool) (string, error) {
	if c == nil {
		return "", core.NewInvalidRequestError("invalid passthrough request context", nil)
	}
	_, rawEndpoint, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path)
	if !ok {
		return "", core.NewInvalidRequestError("invalid provider passthrough path", nil)
	}
	endpoint, err := normalizePassthroughEndpoint(rawEndpoint, allowV1Alias)
	if err != nil {
		return "", err
	}
	return finalizePassthroughEndpoint(endpoint, c.Request().URL.RawQuery)
}

func passthroughExecutionTarget(c *echo.Context, provider core.RoutableProvider, allowPassthroughV1Alias bool) (string, string, *core.PassthroughRouteInfo, error) {
	if c == nil {
		return "", "", nil, core.NewInvalidRequestError("invalid provider passthrough path", nil)
	}

	info := passthroughRouteInfo(c)
	if info == nil {
		return "", "", nil, core.NewInvalidRequestError("invalid provider passthrough path", nil)
	}

	providerType := strings.TrimSpace(resolvePassthroughProvider(provider, info.Provider).ProviderType)
	if providerType == "" {
		if workflow := core.GetWorkflow(c.Request().Context()); workflow != nil {
			providerType = strings.TrimSpace(workflow.ProviderType)
		}
	}
	if providerType == "" {
		return "", "", nil, core.NewInvalidRequestError("invalid provider passthrough path", nil)
	}

	endpoint := strings.TrimSpace(info.NormalizedEndpoint)
	if endpoint == "" {
		var err error
		endpoint, err = normalizePassthroughEndpoint(info.RawEndpoint, allowPassthroughV1Alias)
		if err != nil {
			return "", "", nil, err
		}
		info.NormalizedEndpoint = endpoint
	}
	endpoint, err := finalizePassthroughEndpoint(endpoint, c.Request().URL.RawQuery)
	if err != nil {
		return "", "", nil, err
	}

	info.Provider = providerType
	return providerType, endpoint, info, nil
}
