package responsestore

import (
	"errors"
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage"
)

const (
	// DefaultPersistentStoreTTL bounds stored response retention in persistent
	// backends. Matches OpenAI's documented 30-day retention for stored
	// responses; expired rows are swept hourly.
	DefaultPersistentStoreTTL = 30 * 24 * time.Hour

	// CleanupInterval is how often persistent stores sweep expired snapshots.
	CleanupInterval = 1 * time.Hour
)

// prepareStoredResponseForStorage validates, normalizes, and — when stamp is
// true — applies StoredAt/ExpiresAt defaults before serializing the snapshot.
// Create paths stamp retention; Update paths do not, so existing column values
// are preserved for zero fields (mirroring the memory store).
func prepareStoredResponseForStorage(response *StoredResponse, now time.Time, ttl time.Duration, stamp bool) (*StoredResponse, []byte, error) {
	if response == nil || response.Response == nil || response.Response.ID == "" {
		return nil, nil, fmt.Errorf("response id is required")
	}
	normalized := normalizeStoredResponse(response)
	if stamp {
		if normalized.StoredAt.IsZero() {
			normalized.StoredAt = now
		}
		if ttl > 0 && normalized.ExpiresAt.IsZero() {
			normalized.ExpiresAt = normalized.StoredAt.Add(ttl)
		}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal response: %w", err)
	}
	return normalized, data, nil
}

// scanStoredResponseRow converts one (data, stored_at, expires_at) row into a
// StoredResponse, mapping the backend's no-rows sentinel to ErrNotFound and
// treating expired rows as absent.
func scanStoredResponseRow(row storage.RowScanner, noRows error) (*StoredResponse, error) {
	var (
		data                string
		storedAt, expiresAt int64
	)
	if err := row.Scan(&data, &storedAt, &expiresAt); err != nil {
		if errors.Is(err, noRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query response snapshot: %w", err)
	}
	if expiresAt > 0 && expiresAt <= time.Now().Unix() {
		return nil, ErrNotFound
	}
	return decodeStoredResponse([]byte(data), storedAt, expiresAt)
}

// decodeStoredResponse deserializes a snapshot and applies the authoritative
// retention columns over whatever the serialized copy carries.
func decodeStoredResponse(data []byte, storedAt, expiresAt int64) (*StoredResponse, error) {
	var stored StoredResponse
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	stored.StoredAt = storage.UnixTime(storedAt)
	stored.ExpiresAt = storage.UnixTime(expiresAt)
	return &stored, nil
}
