package server

import (
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/anthropicapi"
	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// Messages handles POST /v1/messages.
//
// It accepts the Anthropic Messages API request dialect, translates it to the
// canonical chat request, and runs it through the standard chat-completions
// pipeline so it routes to any configured provider with full workflow, budget,
// failover, cache, usage, and audit support. See ADR-0007.
//
// @Summary      Create a message (Anthropic Messages API)
// @Tags         messages
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      anthropicapi.MessagesRequest  true  "Anthropic Messages request"
// @Success      200      {object}  anthropicapi.MessagesResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  anthropicapi.ErrorResponse
// @Failure      401      {object}  anthropicapi.ErrorResponse
// @Failure      429      {object}  anthropicapi.ErrorResponse
// @Failure      502      {object}  anthropicapi.ErrorResponse
// @Router       /v1/messages [post]
func (h *Handler) Messages(c *echo.Context) error {
	return h.translatedInference().Messages(c)
}

// CountMessageTokens handles POST /v1/messages/count_tokens.
//
// @Summary      Count message tokens (Anthropic Messages API)
// @Description  Returns a provider-agnostic heuristic estimate of the input token count.
// @Tags         messages
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      anthropicapi.MessagesRequest  true  "Anthropic Messages request"
// @Success      200      {object}  anthropicapi.CountTokensResponse
// @Failure      400      {object}  anthropicapi.ErrorResponse
// @Failure      401      {object}  anthropicapi.ErrorResponse
// @Router       /v1/messages/count_tokens [post]
func (h *Handler) CountMessageTokens(c *echo.Context) error {
	return h.translatedInference().CountMessageTokens(c)
}

// Messages translates an Anthropic Messages request and dispatches it through
// the shared chat-completions pipeline (workflow resolution, response cache).
func (s *translatedInferenceService) Messages(c *echo.Context) error {
	req, err := decodeMessagesChatRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	ctx, prepared, workflow, err := prepareChatCompletionRequest(s, c.Request().Context(), req, translatedRequestMeta(c))
	if err != nil {
		return handleError(c, err)
	}
	attachPreparedWorkflow(c, ctx, workflow)

	return handleWithCache(s, c, prepared, workflow, s.dispatchMessages)
}

// CountMessageTokens returns a heuristic input token estimate for a Messages
// request. It performs no provider call (see ADR-0007).
func (s *translatedInferenceService) CountMessageTokens(c *echo.Context) error {
	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	req, err := anthropicapi.DecodeMessagesRequest(body)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if strings.TrimSpace(req.Model) == "" {
		return handleError(c, core.NewInvalidRequestError("model is required", nil).WithParam("model"))
	}
	return c.JSON(http.StatusOK, anthropicapi.CountTokensResponse{
		InputTokens: anthropicapi.EstimateInputTokens(req),
	})
}

func (s *translatedInferenceService) dispatchMessages(c *echo.Context, req *core.ChatRequest, workflow *core.Workflow) error {
	s.observeLiveProviderAttempts(c, workflow)
	ctx := c.Request().Context()
	requestID := requestIDFromContextOrHeader(c.Request())

	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return handleError(c, err)
	}

	if req.Stream {
		result, err := s.inference().StreamChatCompletion(ctx, workflow, req)
		if err != nil {
			return handleStreamingDispatchError(c, err)
		}
		if result.Meta.UsedFallback {
			markRequestFallbackUsed(c)
		}
		model := result.Meta.Model
		return s.handleStreamingReadCloser(
			c,
			workflow,
			model,
			result.Meta.ProviderType,
			result.Meta.ProviderName,
			result.Meta.FailoverModel,
			result.Stream,
			func(stream io.ReadCloser) io.ReadCloser {
				return anthropicapi.NewStreamConverter(stream, model)
			},
		)
	}

	result, err := s.inference().ExecuteChatCompletion(ctx, workflow, req, requestID, "/v1/messages")
	if err != nil {
		return handleError(c, err)
	}
	enrichAuditEntryWithProviderAttempts(c)
	if result.Meta.UsedFallback {
		markRequestFallbackUsed(c)
		auditlog.EnrichEntryWithFailover(c, result.Meta.FailoverModel)
	}
	auditlog.EnrichEntryWithResolvedRoute(
		c,
		qualifyExecutedModel(workflow, result.Response.Model, result.Meta.ProviderName),
		result.Meta.ProviderType,
		result.Meta.ProviderName,
	)

	return c.JSON(http.StatusOK, anthropicapi.FromChatResponse(result.Response))
}

// decodeMessagesChatRequest reads the request body, decodes the Anthropic
// Messages request, and translates it to the canonical chat request.
func decodeMessagesChatRequest(c *echo.Context) (*core.ChatRequest, error) {
	body, err := requestBodyBytes(c)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	req, err := anthropicapi.DecodeMessagesRequest(body)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	return anthropicapi.ToChatRequest(req)
}
