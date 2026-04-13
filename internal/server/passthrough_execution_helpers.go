package server

import (
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

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
	if endpoint == "" {
		return "", "", nil, core.NewInvalidRequestError("provider passthrough endpoint is required", nil)
	}
	if rawQuery := strings.TrimSpace(c.Request().URL.RawQuery); rawQuery != "" {
		endpoint += "?" + rawQuery
	}

	info.Provider = providerType
	return providerType, endpoint, info, nil
}
