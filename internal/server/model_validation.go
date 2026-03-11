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

			model, providerHint, parsed, err := selectorHintsForValidation(c)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError(err.Error(), err))
			}
			if !parsed {
				return next(c)
			}
			selector, err := core.ParseModelSelector(model, providerHint)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError(err.Error(), err))
			}
			if counted, ok := provider.(modelCountProvider); ok && counted.ModelCount() == 0 {
				return handleError(c, core.NewProviderError("", 0, "model registry not initialized", nil))
			}

			if !provider.Supports(selector.QualifiedModel()) {
				return handleError(c, core.NewInvalidRequestError("unsupported model: "+selector.QualifiedModel(), nil))
			}

			providerType := provider.GetProviderType(selector.QualifiedModel())
			c.Set(string(providerTypeKey), providerType)
			auditlog.EnrichEntry(c, selector.Model, providerType)

			requestID := c.Request().Header.Get("X-Request-ID")
			ctx := core.WithRequestID(c.Request().Context(), requestID)
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}

func selectorHintsForValidation(c *echo.Context) (model, provider string, parsed bool, err error) {
	ctx := c.Request().Context()
	if env := core.GetSemanticEnvelope(ctx); env != nil {
		if model, provider, ok := cachedCanonicalSelectorHints(env); ok {
			return model, provider, true, nil
		}
		if model, provider, ok := decodeCanonicalSelectorHintsForValidation(ctx, env); ok {
			return model, provider, true, nil
		}
		if env.JSONBodyParsed || env.SelectorHints.Model != "" || env.SelectorHints.Provider != "" {
			return env.SelectorHints.Model, env.SelectorHints.Provider, true, nil
		}
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return "", "", false, err
	}
	if env := core.GetSemanticEnvelope(ctx); env != nil {
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

func cachedCanonicalSelectorHints(env *core.SemanticEnvelope) (model, provider string, ok bool) {
	return env.CachedCanonicalSelector()
}

func decodeCanonicalSelectorHintsForValidation(ctx context.Context, env *core.SemanticEnvelope) (model, provider string, ok bool) {
	if env == nil {
		return "", "", false
	}
	frame := core.GetIngressFrame(ctx)
	if frame == nil {
		return "", "", false
	}
	rawBody := frame.GetRawBody()
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
	if env := core.GetSemanticEnvelope(c.Request().Context()); env != nil && env.Operation == "provider_passthrough" {
		providerType := strings.TrimSpace(env.SelectorHints.Provider)
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
	return ""
}

// ModelCtx returns the request context and resolved provider type.
func ModelCtx(c *echo.Context) (context.Context, string) {
	return c.Request().Context(), GetProviderType(c)
}
