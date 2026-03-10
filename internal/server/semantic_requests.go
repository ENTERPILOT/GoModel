package server

import (
	"encoding/json"

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

func chatRequestFromSemanticEnvelope(c *echo.Context) (*core.ChatRequest, error) {
	env := ensureSemanticEnvelope(c)
	if env != nil && env.ChatRequest != nil {
		return env.ChatRequest, nil
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, err
	}

	var req core.ChatRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return nil, err
	}
	if env != nil {
		env.ChatRequest = &req
		updateSemanticSelectorHints(env, req.Model, req.Provider)
	}
	return &req, nil
}

func responsesRequestFromSemanticEnvelope(c *echo.Context) (*core.ResponsesRequest, error) {
	env := ensureSemanticEnvelope(c)
	if env != nil && env.ResponsesRequest != nil {
		return env.ResponsesRequest, nil
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, err
	}

	var req core.ResponsesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return nil, err
	}
	if env != nil {
		env.ResponsesRequest = &req
		updateSemanticSelectorHints(env, req.Model, req.Provider)
	}
	return &req, nil
}

func embeddingRequestFromSemanticEnvelope(c *echo.Context) (*core.EmbeddingRequest, error) {
	env := ensureSemanticEnvelope(c)
	if env != nil && env.EmbeddingRequest != nil {
		return env.EmbeddingRequest, nil
	}

	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return nil, err
	}

	var req core.EmbeddingRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return nil, err
	}
	if env != nil {
		env.EmbeddingRequest = &req
		updateSemanticSelectorHints(env, req.Model, req.Provider)
	}
	return &req, nil
}
