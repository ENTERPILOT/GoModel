package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type contextKey string

const providerTypeKey contextKey = "providerType"

// ModelValidation validates model-interaction requests, enriches audit metadata,
// and propagates request-scoped values needed by downstream handlers.
func ModelValidation(provider core.RoutableProvider) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if !auditlog.IsModelInteractionPath(path) {
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
	if env := core.GetSemanticEnvelope(c.Request().Context()); env != nil {
		if env.JSONBodyParsed || env.SelectorHints.Model != "" || env.SelectorHints.Provider != "" {
			return env.SelectorHints.Model, env.SelectorHints.Provider, true, nil
		}
		return "", "", false, nil
	}

	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return "", "", false, err
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var peek struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(bodyBytes, &peek); err != nil {
		return "", "", false, nil
	}
	return peek.Model, peek.Provider, true, nil
}

func isBatchOrFileRootOrSubresource(path string) bool {
	return path == "/v1/batches" ||
		strings.HasPrefix(path, "/v1/batches/") ||
		path == "/v1/files" ||
		strings.HasPrefix(path, "/v1/files/")
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
