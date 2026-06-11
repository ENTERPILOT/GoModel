// Package conversationstore provides persistence for the OpenAI-compatible
// Conversations lifecycle endpoints.
package conversationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gomodel/internal/core"
)

// ErrNotFound indicates a requested conversation was not found.
var ErrNotFound = errors.New("conversation not found")

// StoredConversation keeps the public conversation snapshot separate from
// gateway-only metadata (initial items, owning user path, request id).
type StoredConversation struct {
	Conversation *core.Conversation `json:"conversation"`
	Items        []json.RawMessage  `json:"items,omitempty"`
	UserPath     string             `json:"user_path,omitempty"`
	RequestID    string             `json:"request_id,omitempty"`
	StoredAt     time.Time          `json:"stored_at,omitempty"`
	ExpiresAt    time.Time          `json:"expires_at,omitempty"`
}

// Store defines persistence operations for the Conversations lifecycle API.
type Store interface {
	Create(ctx context.Context, conversation *StoredConversation) error
	Get(ctx context.Context, id string) (*StoredConversation, error)
	Update(ctx context.Context, conversation *StoredConversation) error
	// AppendItems atomically appends items to an existing conversation, so two
	// concurrently completing turns cannot overwrite each other's exchange the
	// way a Get-then-Update would.
	AppendItems(ctx context.Context, id string, items []json.RawMessage) error
	Delete(ctx context.Context, id string) error
	Close() error
}

func cloneConversation(src *StoredConversation) (*StoredConversation, error) {
	if src == nil {
		return nil, fmt.Errorf("conversation is nil")
	}
	normalized := normalizeStoredConversation(src)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal conversation: %w", err)
	}
	var dst StoredConversation
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, fmt.Errorf("unmarshal conversation: %w", err)
	}
	return &dst, nil
}

func normalizeStoredConversation(src *StoredConversation) *StoredConversation {
	if src == nil {
		return nil
	}

	normalized := *src
	normalized.UserPath = strings.TrimSpace(normalized.UserPath)
	normalized.RequestID = strings.TrimSpace(normalized.RequestID)

	if src.Conversation != nil {
		conversationCopy := *src.Conversation
		if conversationCopy.Metadata != nil {
			metadataCopy := make(map[string]string, len(conversationCopy.Metadata))
			for key, value := range conversationCopy.Metadata {
				metadataCopy[key] = value
			}
			conversationCopy.Metadata = metadataCopy
		}
		normalized.Conversation = &conversationCopy
	}

	if len(src.Items) > 0 {
		normalized.Items = make([]json.RawMessage, 0, len(src.Items))
		for _, item := range src.Items {
			normalized.Items = append(normalized.Items, core.CloneRawJSON(item))
		}
	}

	return &normalized
}
