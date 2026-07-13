package conversationstore

import (
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
)

const (
	// DefaultPersistentStoreTTL bounds stored conversation retention in
	// persistent backends, matching the in-memory default; expired rows are
	// swept hourly.
	DefaultPersistentStoreTTL = 30 * 24 * time.Hour

	// CleanupInterval is how often persistent stores sweep expired conversations.
	CleanupInterval = 1 * time.Hour
)

// prepareStoredConversationForStorage validates, normalizes, and — when stamp
// is true — applies StoredAt/ExpiresAt defaults, then serializes the snapshot
// with items split out so backends can append them atomically. Create paths
// stamp retention; Update paths do not, so existing column values are
// preserved for zero fields (mirroring the memory store).
func prepareStoredConversationForStorage(conversation *StoredConversation, now time.Time, ttl time.Duration, stamp bool) (normalized *StoredConversation, data []byte, items []byte, err error) {
	if conversation == nil || conversation.Conversation == nil || conversation.Conversation.ID == "" {
		return nil, nil, nil, fmt.Errorf("conversation id is required")
	}
	normalized = normalizeStoredConversation(conversation)
	if stamp {
		if normalized.StoredAt.IsZero() {
			normalized.StoredAt = now
		}
		if ttl > 0 && normalized.ExpiresAt.IsZero() {
			normalized.ExpiresAt = normalized.StoredAt.Add(ttl)
		}
	}

	items, err = json.Marshal(itemsOrEmpty(normalized.Items))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal conversation items: %w", err)
	}

	// The snapshot column intentionally excludes items — they live in their own
	// column/field so AppendItems can grow them without rewriting the snapshot.
	snapshot := *normalized
	snapshot.Items = nil
	data, err = json.Marshal(&snapshot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal conversation: %w", err)
	}
	return normalized, data, items, nil
}

// scanStoredConversationRow converts one (data, items, stored_at, expires_at)
// row into a StoredConversation, mapping the backend's no-rows sentinel to
// ErrNotFound and treating expired rows as absent.
func scanStoredConversationRow(row storage.RowScanner, noRows error) (*StoredConversation, error) {
	var (
		data, items         string
		storedAt, expiresAt int64
	)
	if err := row.Scan(&data, &items, &storedAt, &expiresAt); err != nil {
		if errors.Is(err, noRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query conversation snapshot: %w", err)
	}
	if expiresAt > 0 && expiresAt <= time.Now().Unix() {
		return nil, ErrNotFound
	}
	return decodeStoredConversation([]byte(data), []byte(items), storedAt, expiresAt)
}

// decodeStoredConversation deserializes a snapshot and its items and applies
// the authoritative retention columns over whatever the serialized copy carries.
func decodeStoredConversation(data, items []byte, storedAt, expiresAt int64) (*StoredConversation, error) {
	var stored StoredConversation
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal conversation: %w", err)
	}
	if len(items) > 0 {
		if err := json.Unmarshal(items, &stored.Items); err != nil {
			return nil, fmt.Errorf("unmarshal conversation items: %w", err)
		}
	}
	stored.StoredAt = storage.UnixTime(storedAt)
	stored.ExpiresAt = storage.UnixTime(expiresAt)
	return &stored, nil
}

func itemsOrEmpty(items []json.RawMessage) []json.RawMessage {
	if items == nil {
		return []json.RawMessage{}
	}
	return items
}
