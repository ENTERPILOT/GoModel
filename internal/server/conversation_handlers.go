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
// @Description  Merges supplied keys into the conversation metadata.
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

// CreateConversationItems handles POST /v1/conversations/{id}/items.
//
// @Summary      Create conversation items
// @Tags         conversations
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string                              true  "Conversation ID"
// @Param        include  query     []string                            false "Additional fields to include"
// @Param        request  body      core.ConversationItemCreateRequest  true  "Conversation items"
// @Success      200      {object}  core.ConversationItemListResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id}/items [post]
func (h *Handler) CreateConversationItems(c *echo.Context) error {
	return h.conversations().CreateConversationItems(c)
}

// ListConversationItems handles GET /v1/conversations/{id}/items.
//
// @Summary      List conversation items
// @Tags         conversations
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string    true  "Conversation ID"
// @Param        after    query     string    false "Return items after this item ID"
// @Param        include  query     []string  false "Additional fields to include"
// @Param        limit    query     int       false "Maximum items" minimum(1) maximum(100) default(20)
// @Param        order    query     string    false "Sort order" Enums(asc,desc) default(desc)
// @Success      200      {object}  core.ConversationItemListResponse
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id}/items [get]
func (h *Handler) ListConversationItems(c *echo.Context) error {
	return h.conversations().ListConversationItems(c)
}

// GetConversationItem handles GET /v1/conversations/{id}/items/{item_id}.
//
// @Summary      Get a conversation item
// @Tags         conversations
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      string    true  "Conversation ID"
// @Param        item_id  path      string    true  "Conversation item ID"
// @Param        include  query     []string  false "Additional fields to include"
// @Success      200      {object}  object
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id}/items/{item_id} [get]
func (h *Handler) GetConversationItem(c *echo.Context) error {
	return h.conversations().GetConversationItem(c)
}

// DeleteConversationItem handles DELETE /v1/conversations/{id}/items/{item_id}.
//
// @Summary      Delete a conversation item
// @Tags         conversations
// @Produce      json
// @Security     BearerAuth
// @Param        id       path  string  true  "Conversation ID"
// @Param        item_id  path  string  true  "Conversation item ID"
// @Success      200      {object}  core.Conversation
// @Failure      400      {object}  core.OpenAIErrorEnvelope
// @Failure      401      {object}  core.OpenAIErrorEnvelope
// @Failure      404      {object}  core.OpenAIErrorEnvelope
// @Router       /v1/conversations/{id}/items/{item_id} [delete]
func (h *Handler) DeleteConversationItem(c *echo.Context) error {
	return h.conversations().DeleteConversationItem(c)
}
