package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
)

// CreateConversationItems handles POST /v1/conversations/{id}/items.
func (s *conversationService) CreateConversationItems(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)
	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	req, err := core.DecodeConversationItemCreateRequest(body)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if len(req.Items) == 0 {
		return handleError(c, core.NewInvalidRequestError("items is required", nil).WithParam("items"))
	}
	if len(req.Items) > core.MaxConversationInitialItems {
		return handleError(c, core.NewInvalidRequestError(
			fmt.Sprintf("items supports at most %d entries", core.MaxConversationInitialItems), nil,
		).WithParam("items"))
	}
	items, normalizeErr := normalizeConversationItems(req.Items)
	if normalizeErr != nil {
		return handleError(c, normalizeErr)
	}
	if err := s.conversationStore.AppendItems(ctx, id, items); err != nil {
		switch {
		case errors.Is(err, conversationstore.ErrNotFound):
			return handleError(c, conversationNotFound(id))
		case errors.Is(err, conversationstore.ErrDuplicateItem):
			return handleError(c, core.NewInvalidRequestError("duplicate conversation item id", err).WithParam("items"))
		}
		return handleError(c, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to append conversation items", err))
	}
	return c.JSON(http.StatusOK, conversationItemList(items, false, conversationItemIncludes(c)))
}

// ListConversationItems handles GET /v1/conversations/{id}/items.
func (s *conversationService) ListConversationItems(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)
	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	params, err := conversationItemListParamsFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	stored, err := s.loadStoredConversation(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	page, pageErr := paginateConversationItems(stored.Items, params)
	if pageErr != nil {
		return handleError(c, pageErr)
	}
	return c.JSON(http.StatusOK, page)
}

// GetConversationItem handles GET /v1/conversations/{id}/items/{item_id}.
func (s *conversationService) GetConversationItem(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)
	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	itemID, err := conversationItemIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	stored, err := s.loadStoredConversation(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	for _, item := range stored.Items {
		if responseInputItemID(item) == itemID {
			return c.JSONBlob(http.StatusOK, conversationItemForInclude(item, conversationItemIncludes(c)))
		}
	}
	return handleError(c, conversationItemNotFound(itemID))
}

// DeleteConversationItem handles DELETE /v1/conversations/{id}/items/{item_id}.
func (s *conversationService) DeleteConversationItem(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)
	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	itemID, err := conversationItemIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	stored, err := s.conversationStore.DeleteItem(ctx, id, itemID)
	if err != nil {
		switch {
		case errors.Is(err, conversationstore.ErrNotFound):
			return handleError(c, conversationNotFound(id))
		case errors.Is(err, conversationstore.ErrItemNotFound):
			return handleError(c, conversationItemNotFound(itemID))
		default:
			return handleError(c, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to delete conversation item", err))
		}
	}
	return c.JSON(http.StatusOK, stored.Conversation)
}

func conversationItemListParamsFromRequest(c *echo.Context) (core.ConversationItemListParams, error) {
	common, err := cursorListParamsFromRequest(c)
	if err != nil {
		return core.ConversationItemListParams{}, err
	}
	return core.ConversationItemListParams{
		After:   common.after,
		Include: common.include,
		Limit:   common.limit,
		Order:   common.order,
	}, nil
}

func conversationItemIDFromRequest(c *echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("item_id"))
	if id == "" {
		return "", core.NewInvalidRequestError("conversation item id is required", nil)
	}
	return id, nil
}

func conversationItemNotFound(id string) *core.GatewayError {
	return core.NewNotFoundError("conversation item not found: " + id)
}
