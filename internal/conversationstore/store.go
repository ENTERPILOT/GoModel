// Package conversationstore provides persistence for the OpenAI-compatible
// Conversations lifecycle endpoints.
package conversationstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

var (
	// ErrNotFound indicates a requested conversation was not found.
	ErrNotFound = errors.New("conversation not found")
	// ErrItemNotFound indicates a requested item was not present in an existing
	// conversation.
	ErrItemNotFound = errors.New("conversation item not found")
	// ErrDuplicateItem indicates an append would introduce an item id already
	// present in the conversation.
	ErrDuplicateItem = errors.New("conversation item id already exists")
	// ErrMetadataLimitExceeded indicates a patch would make the final metadata
	// object exceed the OpenAI Conversations limit.
	ErrMetadataLimitExceeded = errors.New("conversation metadata limit exceeded")
)

// StoredConversation keeps the public conversation snapshot separate from
// gateway-only metadata (initial items, owning user path, request id).
type StoredConversation struct {
	Conversation *core.Conversation `json:"conversation"`
	Items        []json.RawMessage  `json:"items,omitempty"`
	UserPath     string             `json:"user_path,omitempty"`
	RequestID    string             `json:"request_id,omitempty"`
	StoredAt     time.Time          `json:"stored_at"`
	ExpiresAt    time.Time          `json:"expires_at"`
}

// Store defines persistence operations for the Conversations lifecycle API.
type Store interface {
	Create(ctx context.Context, conversation *StoredConversation) error
	Get(ctx context.Context, id string) (*StoredConversation, error)
	// MergeMetadata atomically overlays metadata without rewriting items that a
	// concurrently completing Responses turn may be appending.
	MergeMetadata(ctx context.Context, id string, metadata map[string]string) (*StoredConversation, error)
	// AppendItems atomically appends items to an existing conversation, so two
	// concurrently completing turns cannot overwrite each other's exchange the
	// way a Get-then-Update would.
	AppendItems(ctx context.Context, id string, items []json.RawMessage) error
	// DeleteItem atomically removes one item and returns the updated snapshot.
	DeleteItem(ctx context.Context, id, itemID string) (*StoredConversation, error)
	Delete(ctx context.Context, id string) error
	Close() error
}

func itemID(raw json.RawMessage) string {
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return ""
	}
	return strings.TrimSpace(item.ID)
}

func duplicateItemID(existing, added []json.RawMessage) string {
	ids := make(map[string]struct{}, len(existing)+len(added))
	for _, raw := range existing {
		if id := itemID(raw); id != "" {
			ids[id] = struct{}{}
		}
	}
	for _, raw := range added {
		id := itemID(raw)
		if id == "" {
			continue
		}
		if _, exists := ids[id]; exists {
			return id
		}
		ids[id] = struct{}{}
	}
	return ""
}

func cloneConversation(src *StoredConversation) (*StoredConversation, error) {
	dst, _, err := cloneConversationWithSize(src)
	return dst, err
}

// cloneConversationWithSize deep-copies a snapshot and reports its serialized
// size, which the memory store uses for byte-budget accounting.
func cloneConversationWithSize(src *StoredConversation) (*StoredConversation, int64, error) {
	if src == nil {
		return nil, 0, fmt.Errorf("conversation is nil")
	}
	normalized := normalizeStoredConversation(src)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal conversation: %w", err)
	}
	var dst StoredConversation
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, 0, fmt.Errorf("unmarshal conversation: %w", err)
	}
	return &dst, int64(len(b)), nil
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
			maps.Copy(metadataCopy, conversationCopy.Metadata)
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
