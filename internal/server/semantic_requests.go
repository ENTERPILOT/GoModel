package server

import (
	"encoding/json"
	"strconv"
	"strings"

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

func updateSemanticSelectorHints(env *core.SemanticEnvelope, model, provider string) {
	if env == nil {
		return
	}
	env.JSONBodyParsed = true
	env.SelectorHints.Model = model
	if env.SelectorHints.Provider == "" {
		env.SelectorHints.Provider = provider
	}
}

func decodeJSONRequestFromSemanticEnvelope[T any](
	c *echo.Context,
	current func(*core.SemanticEnvelope) *T,
	cache func(*core.SemanticEnvelope, *T),
) (*T, error) {
	env := ensureSemanticEnvelope(c)
	if env != nil {
		if req := current(env); req != nil {
			return req, nil
		}
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, err
	}

	req := new(T)
	if err := json.Unmarshal(bodyBytes, req); err != nil {
		return nil, err
	}
	if env != nil {
		cache(env, req)
	}
	return req, nil
}

func chatRequestFromSemanticEnvelope(c *echo.Context) (*core.ChatRequest, error) {
	return decodeJSONRequestFromSemanticEnvelope(c,
		func(env *core.SemanticEnvelope) *core.ChatRequest {
			return env.ChatRequest
		},
		func(env *core.SemanticEnvelope, req *core.ChatRequest) {
			env.ChatRequest = req
			updateSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

func responsesRequestFromSemanticEnvelope(c *echo.Context) (*core.ResponsesRequest, error) {
	return decodeJSONRequestFromSemanticEnvelope(c,
		func(env *core.SemanticEnvelope) *core.ResponsesRequest {
			return env.ResponsesRequest
		},
		func(env *core.SemanticEnvelope, req *core.ResponsesRequest) {
			env.ResponsesRequest = req
			updateSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

func embeddingRequestFromSemanticEnvelope(c *echo.Context) (*core.EmbeddingRequest, error) {
	return decodeJSONRequestFromSemanticEnvelope(c,
		func(env *core.SemanticEnvelope) *core.EmbeddingRequest {
			return env.EmbeddingRequest
		},
		func(env *core.SemanticEnvelope, req *core.EmbeddingRequest) {
			env.EmbeddingRequest = req
			updateSemanticSelectorHints(env, req.Model, req.Provider)
		},
	)
}

func batchRequestFromSemanticEnvelope(c *echo.Context) (*core.BatchRequest, error) {
	return decodeJSONRequestFromSemanticEnvelope(c,
		func(env *core.SemanticEnvelope) *core.BatchRequest {
			return env.BatchRequest
		},
		func(env *core.SemanticEnvelope, req *core.BatchRequest) {
			env.BatchRequest = req
			env.JSONBodyParsed = true
		},
	)
}

func batchRequestMetadataFromSemanticEnvelope(c *echo.Context) (*core.BatchRequestSemantic, error) {
	env := ensureSemanticEnvelope(c)

	var req *core.BatchRequestSemantic
	if env != nil && env.BatchMetadata != nil {
		req = env.BatchMetadata
	} else {
		req = core.BuildBatchRequestSemanticFromTransport(
			c.Request().Method,
			c.Request().URL.Path,
			pathValuesToMap(c.PathValues()),
			c.Request().URL.Query(),
		)
		if req == nil {
			req = &core.BatchRequestSemantic{}
		}
	}
	if req.LimitRaw != "" && !req.HasLimit {
		parsed, err := strconv.Atoi(strings.TrimSpace(req.LimitRaw))
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid limit parameter", err)
		}
		req.Limit = parsed
		req.HasLimit = true
	}

	if env != nil {
		env.BatchMetadata = req
	}
	return req, nil
}

func fileRequestFromSemanticEnvelope(c *echo.Context) (*core.FileRequestSemantic, error) {
	env := ensureSemanticEnvelope(c)

	var req *core.FileRequestSemantic
	if env != nil && env.FileRequest != nil {
		req = env.FileRequest
	} else {
		req = core.BuildFileRequestSemanticFromTransport(
			c.Request().Method,
			c.Request().URL.Path,
			pathValuesToMap(c.PathValues()),
			c.Request().URL.Query(),
		)
		if req == nil {
			req = &core.FileRequestSemantic{}
		}
	}

	switch req.Action {
	case core.FileActionCreate:
		if req.Provider == "" {
			req.Provider = strings.TrimSpace(c.FormValue("provider"))
		}
		if req.Purpose == "" {
			req.Purpose = strings.TrimSpace(c.FormValue("purpose"))
		}
		if req.Filename == "" {
			fileHeader, err := c.FormFile("file")
			if err == nil && fileHeader != nil {
				req.Filename = strings.TrimSpace(fileHeader.Filename)
			}
		}
	case core.FileActionList:
		if req.LimitRaw != "" && !req.HasLimit {
			parsed, err := strconv.Atoi(strings.TrimSpace(req.LimitRaw))
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid limit parameter", err)
			}
			req.Limit = parsed
			req.HasLimit = true
		}
	}

	if env != nil {
		env.FileRequest = req
		if req.Provider != "" && env.SelectorHints.Provider == "" {
			env.SelectorHints.Provider = req.Provider
		}
	}
	return req, nil
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
