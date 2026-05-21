package server

import (
	"github.com/labstack/echo/v5"
)

// CreateConversation handles POST /v1/conversations.
//
// Conversations are a gateway-managed resource: GoModel generates the
// conversation id and stores the conversation locally, so the endpoint behaves
// identically regardless of which provider serves model traffic.
//
// @Summary      Create a conversation
// @Tags         conversations
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      core.ConversationCreateRequest  false  "Conversation create request"
// @Success      200      {object}  core.Conversation
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      500      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations [post]
func (h *Handler) CreateConversation(c *echo.Context) error {
	return h.conversations().CreateConversation(c)
}

// GetConversation handles GET /v1/conversations/{id}.
//
// @Summary      Get a conversation
// @Tags         conversations
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      string  true  "Conversation ID"
// @Success      200 {object}  core.Conversation
// @Failure      400 {object}  core.OpenAIErrorEnvelope
// @Failure      401 {object}  core.OpenAIErrorEnvelope
// @Failure      404 {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id} [get]
func (h *Handler) GetConversation(c *echo.Context) error {
	return h.conversations().GetConversation(c)
}

// UpdateConversation handles POST /v1/conversations/{id}.
//
// @Summary      Update a conversation
// @Description  Replaces the conversation metadata in full.
// @Tags         conversations
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string                          true  "Conversation ID"
// @Param        request  body      core.ConversationUpdateRequest  true  "Conversation update request"
// @Success      200      {object}  core.Conversation
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id} [post]
func (h *Handler) UpdateConversation(c *echo.Context) error {
	return h.conversations().UpdateConversation(c)
}

// DeleteConversation handles DELETE /v1/conversations/{id}.
//
// @Summary      Delete a conversation
// @Tags         conversations
// @Produce      json
// @Security     BearerAuth
// @Param        id  path      string  true  "Conversation ID"
// @Success      200 {object}  core.ConversationDeleteResponse
// @Failure      400 {object}  core.OpenAIErrorEnvelope
// @Failure      401 {object}  core.OpenAIErrorEnvelope
// @Failure      404 {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id} [delete]
func (h *Handler) DeleteConversation(c *echo.Context) error {
	return h.conversations().DeleteConversation(c)
}
