package server

import (
	"github.com/labstack/echo/v5"
)

// Responses handles POST /v1/responses
//
// @Summary      Create a model response (Responses API)
// @Tags         responses
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      core.ResponsesRequest  true  "Responses API request"
// @Success      200      {object}  core.ResponsesResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses [post]
func (h *Handler) Responses(c *echo.Context) error {
	return h.translatedInference().Responses(c)
}

// GetResponse handles GET /v1/responses/{id}.
//
// @Summary      Get a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native lookups"
// @Param        include   query     []string false "Fields to include in the response" collectionFormat(multi)
// @Param        include[] query     []string false "Fields to include in the response" collectionFormat(multi)
// @Param        include_obfuscation query bool false "Whether to include obfuscated response data"
// @Param        starting_after query int false "Input item offset for providers that support it"
// @Success      200       {object}  core.ResponsesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id} [get]
func (h *Handler) GetResponse(c *echo.Context) error {
	return h.nativeResponses().GetResponse(c)
}

// ListResponseInputItems handles GET /v1/responses/{id}/input_items.
//
// @Summary      List response input items
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native lookups"
// @Param        after     query     string  false  "Pagination cursor"
// @Param        include   query     []string false "Fields to include in listed input items" collectionFormat(multi)
// @Param        include[] query     []string false "Fields to include in listed input items" collectionFormat(multi)
// @Param        limit     query     int     false  "Maximum items to return (1-100, default 20)"
// @Param        order     query     string  false  "Sort order: asc or desc"  Enums(asc, desc)
// @Success      200       {object}  core.ResponseInputItemListResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id}/input_items [get]
func (h *Handler) ListResponseInputItems(c *echo.Context) error {
	return h.nativeResponses().ListResponseInputItems(c)
}

// CancelResponse handles POST /v1/responses/{id}/cancel.
//
// @Summary      Cancel a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native cancellation"
// @Success      200       {object}  core.ResponsesResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id}/cancel [post]
func (h *Handler) CancelResponse(c *echo.Context) error {
	return h.nativeResponses().CancelResponse(c)
}

// DeleteResponse handles DELETE /v1/responses/{id}.
//
// @Summary      Delete a response
// @Tags         responses
// @Produce      json
// @Security     BearerAuth
// @Param        id        path      string  true   "Response ID"
// @Param        provider  query     string  false  "Provider override for native deletion"
// @Success      200       {object}  core.ResponseDeleteResponse
// @Failure      400       {object}  core.OpenAIErrorEnvelope
// @Failure      401       {object}  core.OpenAIErrorEnvelope
// @Failure      404       {object}  core.OpenAIErrorEnvelope
// @Failure      501       {object}  core.OpenAIErrorEnvelope
// @Failure      502       {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/{id} [delete]
func (h *Handler) DeleteResponse(c *echo.Context) error {
	return h.nativeResponses().DeleteResponse(c)
}

// ResponseInputTokens handles POST /v1/responses/input_tokens.
//
// @Summary      Count response input tokens
// @Tags         responses
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ResponseInputTokensRequest  true  "Response input token request"
// @Success      200      {object}  core.ResponseInputTokensResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      501      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/input_tokens [post]
func (h *Handler) ResponseInputTokens(c *echo.Context) error {
	return h.nativeResponses().CountResponseInputTokens(c)
}

// CompactResponse handles POST /v1/responses/compact.
//
// @Summary      Compact response input
// @Tags         responses
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ResponseCompactRequest  true  "Response compact request"
// @Success      200      {object}  core.ResponseCompactResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      501      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/responses/compact [post]
func (h *Handler) CompactResponse(c *echo.Context) error {
	return h.nativeResponses().CompactResponse(c)
}
