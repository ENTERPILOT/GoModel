package server

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
)

// ChatCompletion handles POST /v1/chat/completions
//
// @Summary      Create a chat completion
// @Tags         chat
// @Accept       json
// @Produce      json
// @Produce      text/event-stream
// @Security     BearerAuth
// @Param        request  body      core.ChatRequest  true  "Chat completion request"
// @Success      200      {object}  core.ChatResponse  "JSON response or SSE stream when stream=true"
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/chat/completions [post]
func (h *Handler) ChatCompletion(c *echo.Context) error {
	return h.translatedInference().ChatCompletion(c)
}

// Health handles GET /health
//
// @Summary      Health check
// @Tags         system
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /health [get]
func (h *Handler) Health(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// ListModels handles GET /v1/models
//
// @Summary      List available models
// @Tags         models
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  core.ModelsResponse
// @Failure      401  {object}  core.OpenAIErrorEnvelope
// @Failure      502  {object}  core.OpenAIErrorEnvelope
// @Router       /v1/models [get]
func (h *Handler) ListModels(c *echo.Context) error {
	// /v1/models is not a model-interaction endpoint, so the snapshot
	// middleware does not lift the user-path header into the context. A
	// managed key's bound path (set by auth middleware) still wins; the header
	// only fills in for header-only and master-key callers, so the list is
	// filtered by the same user-path policies inference enforces.
	if ok, err := applyUserPathHeaderToContext(c); !ok {
		return err
	}

	resp, err := h.visibleModels(c)
	if err != nil {
		return handleError(c, err)
	}

	// The models route is shared by both wire dialects. Anthropic SDK clients
	// are identified by the anthropic-version header they always send; render
	// the Anthropic list shape for them, the OpenAI shape for everyone else.
	if c.Request().Header.Get("anthropic-version") != "" {
		var models []core.Model
		if resp != nil {
			models = resp.Data
		}
		return c.JSON(http.StatusOK, anthropicapi.FromModels(models))
	}

	return c.JSON(http.StatusOK, resp)
}

// Embeddings handles POST /v1/embeddings
//
// @Summary      Create embeddings
// @Tags         embeddings
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.EmbeddingRequest  true  "Embeddings request"
// @Success      200      {object}  core.EmbeddingResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      429      {object}  core.OpenAIErrorEnvelope
// @Failure      502      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/embeddings [post]
func (h *Handler) Embeddings(c *echo.Context) error {
	return h.translatedInference().Embeddings(c)
}
