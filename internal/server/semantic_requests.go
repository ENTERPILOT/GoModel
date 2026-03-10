package server

import (
	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

func ensureSemanticEnvelope(c *echo.Context) *core.SemanticEnvelope {
	ctx := c.Request().Context()
	if env := core.GetSemanticEnvelope(ctx); env != nil {
		return env
	}

	frame := core.GetIngressFrame(ctx)
	if frame == nil {
		return nil
	}

	env := core.BuildSemanticEnvelope(frame)
	if env == nil {
		return nil
	}

	c.SetRequest(c.Request().WithContext(core.WithSemanticEnvelope(ctx, env)))
	return env
}

func semanticJSONBody(c *echo.Context) ([]byte, *core.SemanticEnvelope, error) {
	env := ensureSemanticEnvelope(c)
	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, env, err
	}
	return bodyBytes, env, nil
}

func canonicalJSONRequestFromSemanticEnvelope[T any](c *echo.Context, decode func([]byte, *core.SemanticEnvelope) (T, error)) (T, error) {
	bodyBytes, env, err := semanticJSONBody(c)
	if err != nil {
		var zero T
		return zero, err
	}
	return decode(bodyBytes, env)
}

func batchRequestMetadataFromSemanticEnvelope(c *echo.Context) (*core.BatchRequestSemantic, error) {
	return core.BatchRouteMetadata(
		ensureSemanticEnvelope(c),
		c.Request().Method,
		c.Request().URL.Path,
		pathValuesToMap(c.PathValues()),
		c.Request().URL.Query(),
	)
}

func fileRequestFromSemanticEnvelope(c *echo.Context) (*core.FileRequestSemantic, error) {
	env := ensureSemanticEnvelope(c)
	req, err := core.FileRouteMetadata(
		env,
		c.Request().Method,
		c.Request().URL.Path,
		pathValuesToMap(c.PathValues()),
		c.Request().URL.Query(),
	)
	if err != nil {
		return nil, err
	}
	req = core.EnrichFileCreateRequestSemantic(req, echoFileMultipartReader{ctx: c})
	core.CacheFileRequestSemantic(env, req)
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

func pathValuesToMap(values echo.PathValues) map[string]string {
	if len(values) == 0 {
		return nil
	}
	params := make(map[string]string, len(values))
	for _, item := range values {
		params[item.Name] = item.Value
	}
	return params
}
