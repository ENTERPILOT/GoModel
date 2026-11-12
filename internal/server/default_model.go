package server

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
)

// defaultModelResolver is an optional RequestModelResolver capability: the
// model that serves a request naming none, taken from the policy of the
// caller's user path. An empty result keeps the request without a model.
type defaultModelResolver interface {
	DefaultModel(ctx context.Context) string
}

// modelOrPathDefault returns model, or the default model of the caller's
// user path when a text generation request (Chat Completions, Responses,
// Anthropic Messages) names neither a model nor a provider. The default is
// then resolved exactly like a model the client sent. Every other endpoint,
// and every caller without a configured default, keeps the usual "model is
// required" answer.
func modelOrPathDefault(c *echo.Context, resolver RequestModelResolver, model, providerHint string) string {
	if strings.TrimSpace(model) != "" || strings.TrimSpace(providerHint) != "" {
		return model
	}
	switch core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path).Operation {
	case core.OperationChatCompletions, core.OperationResponses:
	default:
		return model
	}
	defaults, ok := resolver.(defaultModelResolver)
	if !ok {
		return model
	}
	return defaults.DefaultModel(c.Request().Context())
}

// fillMessagesDefaultModel gives a Messages request that names no model the
// default workflow resolution chose for it, and reports whether it did. The
// Messages translation requires a model, unlike the OpenAI-dialect requests,
// whose model is filled in from the resolution during preparation.
func fillMessagesDefaultModel(c *echo.Context, req *anthropicapi.MessagesRequest) bool {
	if req == nil || strings.TrimSpace(req.Model) != "" {
		return false
	}
	resolution := currentRequestModelResolution(c)
	if resolution == nil || resolution.Requested.Model == "" {
		return false
	}
	req.Model = resolution.Requested.Model
	return true
}
