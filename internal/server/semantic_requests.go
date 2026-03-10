package server

import (
	"encoding/json"
	"net/http"
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
	fromEnvelope := env != nil && env.BatchMetadata != nil
	if fromEnvelope {
		req = env.BatchMetadata
	} else {
		req = &core.BatchRequestSemantic{}
	}

	if req.Action == "" {
		req.Action = batchActionFromRequest(c.Request().Method, c.Request().URL.Path)
	}

	switch req.Action {
	case core.BatchActionList:
		if req.After == "" && !fromEnvelope {
			req.After = strings.TrimSpace(c.QueryParam("after"))
		}
		if !req.HasLimit {
			raw := strings.TrimSpace(req.LimitRaw)
			if raw == "" && !fromEnvelope {
				raw = strings.TrimSpace(c.QueryParam("limit"))
			}
			if raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil {
					return nil, core.NewInvalidRequestError("invalid limit parameter", err)
				}
				req.Limit = parsed
				req.HasLimit = true
				req.LimitRaw = raw
			}
		}
	default:
		if req.BatchID == "" && !fromEnvelope {
			req.BatchID = strings.TrimSpace(c.Param("id"))
		}
	}

	if env != nil {
		env.BatchMetadata = req
	}
	return req, nil
}

func fileRequestFromSemanticEnvelope(c *echo.Context) (*core.FileRequestSemantic, error) {
	env := ensureSemanticEnvelope(c)

	var req *core.FileRequestSemantic
	fromEnvelope := env != nil && env.FileRequest != nil
	if fromEnvelope {
		req = env.FileRequest
	} else {
		req = &core.FileRequestSemantic{}
	}

	if req.Action == "" {
		req.Action = fileActionFromRequest(c.Request().Method, c.Request().URL.Path)
	}

	if req.Provider == "" && !fromEnvelope {
		if provider := strings.TrimSpace(c.QueryParam("provider")); provider != "" {
			req.Provider = provider
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
		if req.Purpose == "" && !fromEnvelope {
			req.Purpose = strings.TrimSpace(c.QueryParam("purpose"))
		}
		if req.After == "" && !fromEnvelope {
			req.After = strings.TrimSpace(c.QueryParam("after"))
		}
		if !req.HasLimit {
			raw := strings.TrimSpace(req.LimitRaw)
			if raw == "" && !fromEnvelope {
				raw = strings.TrimSpace(c.QueryParam("limit"))
			}
			if raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil {
					return nil, core.NewInvalidRequestError("invalid limit parameter", err)
				}
				req.Limit = parsed
				req.HasLimit = true
				req.LimitRaw = raw
			}
		}
	default:
		if req.FileID == "" && !fromEnvelope {
			req.FileID = strings.TrimSpace(c.Param("id"))
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

func batchActionFromRequest(method, path string) string {
	switch {
	case path == "/v1/batches" && method == http.MethodPost:
		return core.BatchActionCreate
	case path == "/v1/batches" && method == http.MethodGet:
		return core.BatchActionList
	case strings.HasSuffix(path, "/results") && strings.HasPrefix(path, "/v1/batches/") && method == http.MethodGet:
		return core.BatchActionResults
	case strings.HasSuffix(path, "/cancel") && strings.HasPrefix(path, "/v1/batches/") && method == http.MethodPost:
		return core.BatchActionCancel
	case strings.HasPrefix(path, "/v1/batches/") && method == http.MethodGet:
		return core.BatchActionGet
	default:
		return ""
	}
}

func fileActionFromRequest(method, path string) string {
	switch {
	case path == "/v1/files" && method == "POST":
		return core.FileActionCreate
	case path == "/v1/files" && method == "GET":
		return core.FileActionList
	case strings.HasSuffix(path, "/content") && method == "GET":
		return core.FileActionContent
	case strings.HasPrefix(path, "/v1/files/") && method == "GET":
		return core.FileActionGet
	case strings.HasPrefix(path, "/v1/files/") && method == "DELETE":
		return core.FileActionDelete
	default:
		return ""
	}
}
