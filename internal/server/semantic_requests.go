package server

import (
	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func ensureWhiteBoxPrompt(c *echo.Context) *core.WhiteBoxPrompt {
	ctx := c.Request().Context()
	if semantics := core.GetWhiteBoxPrompt(ctx); semantics != nil {
		return semantics
	}

	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil {
		return nil
	}

	semantics := core.DeriveWhiteBoxPrompt(snapshot)
	if semantics == nil {
		return nil
	}

	c.SetRequest(c.Request().WithContext(core.WithWhiteBoxPrompt(ctx, semantics)))
	return semantics
}

func semanticJSONBody(c *echo.Context) ([]byte, *core.WhiteBoxPrompt, error) {
	env := ensureWhiteBoxPrompt(c)
	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, env, err
	}
	return bodyBytes, env, nil
}

func canonicalJSONRequestFromSemantics[T any](c *echo.Context, decode func([]byte, *core.WhiteBoxPrompt) (T, error)) (T, error) {
	bodyBytes, env, err := semanticJSONBody(c)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode(bodyBytes, env)
}

func batchRouteInfoFromSemantics(c *echo.Context) (*core.BatchRouteInfo, error) {
	return core.BatchRouteMetadata(
		ensureWhiteBoxPrompt(c),
		c.Request().Method,
		c.Request().URL.Path,
		routeParamsMap(c.PathValues()),
		c.Request().URL.Query(),
	)
}

func fileRouteInfoFromSemantics(c *echo.Context) (*core.FileRouteInfo, error) {
	env := ensureWhiteBoxPrompt(c)
	req, err := core.FileRouteMetadata(
		env,
		c.Request().Method,
		c.Request().URL.Path,
		routeParamsMap(c.PathValues()),
		c.Request().URL.Query(),
	)
	if err != nil {
		return nil, err
	}
	if req != nil && req.Action == core.FileActionCreate {
		req = core.EnrichFileCreateRouteInfo(req, echoFileMultipartReader{ctx: c})
	}
	core.CacheFileRouteInfo(env, req)
	return req, nil
}

type echoFileMultipartReader struct {
	ctx *echo.Context
}

func (r echoFileMultipartReader) Value(name string) string {
	if r.ctx == nil {
		return ""
	}
	return r.ctx.FormValue(name)
}

func (r echoFileMultipartReader) Filename(name string) (string, bool) {
	if r.ctx == nil {
		return "", false
	}
	fileHeader, err := r.ctx.FormFile(name)
	if err != nil || fileHeader == nil {
		return "", false
	}
	return fileHeader.Filename, true
}
