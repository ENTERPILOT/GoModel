package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/conversationstore"
	"github.com/enterpilot/gomodel/internal/core"
)

// conversationService owns the gateway-managed Conversations lifecycle endpoints.
// Conversations are stored locally rather than proxied: the resource is
// OpenAI-specific, so a gateway-owned store keeps /v1/conversations available
// uniformly regardless of which provider routes model traffic.
type conversationService struct {
	conversationStore conversationstore.Store
}

// CreateConversation handles POST /v1/conversations.
func (s *conversationService) CreateConversation(c *echo.Context) error {
	ctx, requestID := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)

	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	req, err := core.DecodeConversationCreateRequest(body)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if len(req.Items) > core.MaxConversationInitialItems {
		return handleError(c, core.NewInvalidRequestError(
			fmt.Sprintf("items supports at most %d entries", core.MaxConversationInitialItems), nil,
		).WithParam("items"))
	}
	if verr := core.ValidateConversationMetadata(req.Metadata); verr != nil {
		return handleError(c, verr)
	}

	now := time.Now().UTC()
	conversation := &core.Conversation{
		ID:        generatedConversationID(),
		Object:    core.ConversationObject,
		CreatedAt: now.Unix(),
		Metadata:  normalizedConversationMetadata(req.Metadata),
	}
	stored := &conversationstore.StoredConversation{
		Conversation: conversation,
		Items:        cloneRawConversationItems(req.Items),
		UserPath:     core.UserPathFromContext(ctx),
		RequestID:    requestID,
		StoredAt:     now,
	}
	if err := s.conversationStore.Create(ctx, stored); err != nil {
		return handleError(c, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to persist conversation", err))
	}
	return c.JSON(http.StatusOK, conversation)
}

// GetConversation handles GET /v1/conversations/{id}.
func (s *conversationService) GetConversation(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)

	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	stored, err := s.loadStoredConversation(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, stored.Conversation)
}

// UpdateConversation handles POST /v1/conversations/{id}. The metadata in the
// request replaces the conversation's metadata in full, matching OpenAI.
func (s *conversationService) UpdateConversation(c *echo.Context) error {
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
	req, err := core.DecodeConversationUpdateRequest(body)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if req.Metadata == nil {
		return handleError(c, core.NewInvalidRequestError("metadata is required", nil).WithParam("metadata"))
	}
	if verr := core.ValidateConversationMetadata(*req.Metadata); verr != nil {
		return handleError(c, verr)
	}

	stored, err := s.loadStoredConversation(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	stored.Conversation.Metadata = normalizedConversationMetadata(*req.Metadata)
	if err := s.conversationStore.Update(ctx, stored); err != nil {
		if errors.Is(err, conversationstore.ErrNotFound) {
			return handleError(c, conversationNotFound(id))
		}
		return handleError(c, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to update conversation", err))
	}
	return c.JSON(http.StatusOK, stored.Conversation)
}

// DeleteConversation handles DELETE /v1/conversations/{id}.
func (s *conversationService) DeleteConversation(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())
	auditConversationEntry(c)

	id, err := conversationIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}
	if err := s.conversationStore.Delete(ctx, id); err != nil {
		if errors.Is(err, conversationstore.ErrNotFound) {
			return handleError(c, conversationNotFound(id))
		}
		return handleError(c, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to delete conversation", err))
	}
	return c.JSON(http.StatusOK, &core.ConversationDeleteResponse{
		ID:      id,
		Object:  core.ConversationDeletedObject,
		Deleted: true,
	})
}

func (s *conversationService) loadStoredConversation(ctx context.Context, id string) (*conversationstore.StoredConversation, error) {
	if s.conversationStore == nil {
		return nil, conversationNotFound(id)
	}
	stored, err := s.conversationStore.Get(ctx, id)
	if err != nil {
		if errors.Is(err, conversationstore.ErrNotFound) {
			return nil, conversationNotFound(id)
		}
		return nil, core.NewProviderError("conversation_store", http.StatusInternalServerError, "failed to load conversation", err)
	}
	if stored == nil || stored.Conversation == nil {
		return nil, core.NewProviderError("conversation_store", http.StatusInternalServerError, "stored conversation payload missing", nil)
	}
	return stored, nil
}

func conversationIDFromRequest(c *echo.Context) (string, error) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return "", core.NewInvalidRequestError("conversation id is required", nil)
	}
	return id, nil
}

func conversationNotFound(id string) *core.GatewayError {
	return core.NewNotFoundError("conversation not found: " + id)
}

func generatedConversationID() string {
	return "conv_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// normalizedConversationMetadata always returns a non-nil map so the serialized
// conversation carries an empty object rather than null, matching OpenAI.
func normalizedConversationMetadata(metadata map[string]string) map[string]string {
	normalized := make(map[string]string, len(metadata))
	maps.Copy(normalized, metadata)
	return normalized
}

func cloneRawConversationItems(items []json.RawMessage) []json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, core.CloneRawJSON(item))
	}
	return cloned
}

func auditConversationEntry(c *echo.Context) {
	auditlog.EnrichEntry(c, "conversation", "")
}
