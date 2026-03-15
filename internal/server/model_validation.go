package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type contextKey string

const providerTypeKey contextKey = "providerType"

type modelCountProvider interface {
	ModelCount() int
}

// ModelValidation validates model-interaction requests, enriches audit metadata,
// and propagates request-scoped values needed by downstream handlers.
func ModelValidation(provider core.RoutableProvider) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if !core.IsModelInteractionPath(path) {
				return next(c)
			}
			if providerType, ok := providerPassthroughType(c); ok {
				c.Set(string(providerTypeKey), providerType)
				auditlog.EnrichEntry(c, "passthrough", providerType)
				requestID := c.Request().Header.Get("X-Request-ID")
				ctx := core.WithRequestID(c.Request().Context(), requestID)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}
			if isBatchOrFileRootOrSubresource(path) {
				requestID := c.Request().Header.Get("X-Request-ID")
				ctx := core.WithRequestID(c.Request().Context(), requestID)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}

			resolution, parsed, err := ensureRequestModelResolution(c, provider)
			if err != nil {
				return handleError(c, err)
			}
			if !parsed || resolution == nil {
				return next(c)
			}
			if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
				return handleError(c, core.NewProviderError("", 0, "model registry not initialized", nil))
			}

			c.Set(string(providerTypeKey), resolution.ProviderType)
			auditlog.EnrichEntry(c, auditModelName(resolution), resolution.ProviderType)

			requestID := c.Request().Header.Get("X-Request-ID")
			ctx := core.WithRequestID(c.Request().Context(), requestID)
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}

func selectorHintsForValidation(c *echo.Context) (model, provider string, parsed bool, err error) {
	ctx := c.Request().Context()
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		if model, provider, ok := cachedCanonicalSelectorHints(env); ok {
			return model, provider, true, nil
		}
		if model, provider, ok := decodeCanonicalSelectorHintsForValidation(ctx, env); ok {
			return model, provider, true, nil
		}
		if env.JSONBodyParsed || env.RouteHints.Model != "" || env.RouteHints.Provider != "" {
			return env.RouteHints.Model, env.RouteHints.Provider, true, nil
		}
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return "", "", false, err
	}
	if env := core.GetWhiteBoxPrompt(ctx); env != nil {
		if model, provider, ok := core.DecodeCanonicalSelector(bodyBytes, env); ok {
			return model, provider, true, nil
		}
	}

	var peek struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(bodyBytes, &peek); err != nil {
		return "", "", false, nil
	}
	return peek.Model, peek.Provider, true, nil
}

func cachedCanonicalSelectorHints(env *core.WhiteBoxPrompt) (model, provider string, ok bool) {
	return env.CanonicalSelectorFromCachedRequest()
}

func decodeCanonicalSelectorHintsForValidation(ctx context.Context, env *core.WhiteBoxPrompt) (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil {
		return "", "", false
	}
	rawBody := snapshot.CapturedBody()
	if rawBody == nil {
		return "", "", false
	}
	return core.DecodeCanonicalSelector(rawBody, env)
}

func isBatchOrFileRootOrSubresource(path string) bool {
	switch core.DescribeEndpointPath(path).Operation {
	case "batches", "files":
		return true
	default:
		return false
	}
}

func providerPassthroughType(c *echo.Context) (string, bool) {
	if env := core.GetWhiteBoxPrompt(c.Request().Context()); env != nil && env.OperationType == "provider_passthrough" {
		providerType := strings.TrimSpace(env.RouteHints.Provider)
		if providerType != "" {
			return providerType, true
		}
	}
	if providerType, _, ok := core.ParseProviderPassthroughPath(c.Request().URL.Path); ok {
		return providerType, true
	}
	return "", false
}

// GetProviderType returns the provider type set by ModelValidation for this request.
func GetProviderType(c *echo.Context) string {
	if v, ok := c.Get(string(providerTypeKey)).(string); ok {
		return v
	}
	if resolution := core.GetRequestModelResolution(c.Request().Context()); resolution != nil {
		return resolution.ProviderType
	}
	return ""
}

// ModelCtx returns the request context and resolved provider type.
func ModelCtx(c *echo.Context) (context.Context, string) {
	return c.Request().Context(), GetProviderType(c)
}
