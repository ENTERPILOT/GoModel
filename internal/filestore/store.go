// Package filestore persists provider ownership for uploaded files.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// ErrNotFound indicates a requested file mapping was not found.
var ErrNotFound = errors.New("file mapping not found")

// StoredFile maps an OpenAI-compatible file ID to the provider that owns it.
type StoredFile struct {
	ID           string `json:"id" bson:"_id"`
	ProviderType string `json:"provider_type" bson:"provider_type"`
	Purpose      string `json:"purpose,omitempty" bson:"purpose,omitempty"`
	Filename     string `json:"filename,omitempty" bson:"filename,omitempty"`
	Bytes        int64  `json:"bytes,omitempty" bson:"bytes,omitempty"`
	CreatedAt    int64  `json:"created_at,omitempty" bson:"created_at,omitempty"`
	UserPath     string `json:"user_path,omitempty" bson:"user_path,omitempty"`
}

// Store defines persistence operations for file provider mappings.
type Store interface {
	Upsert(ctx context.Context, file *StoredFile) error
	Get(ctx context.Context, id string) (*StoredFile, error)
	// List returns mappings newest first, starting after the cursor id. The
	// cursor must exist and match the filter, else ErrNotFound.
	List(ctx context.Context, filter ListFilter, limit int, after string) ([]*StoredFile, error)
	Delete(ctx context.Context, id string) error
	Close() error
}

// ListFilter narrows a listing. Empty fields do not filter. UserPath keeps
// the mappings inside one subtree: the path itself and its descendants; root
// ("/") keeps every mapping that carries a path.
type ListFilter struct {
	UserPath     string
	ProviderType string
	Purpose      string
}

func (f ListFilter) matches(file *StoredFile) bool {
	if file == nil {
		return false
	}
	if f.UserPath != "" && !core.UserPathContains(f.UserPath, file.UserPath) {
		return false
	}
	if f.ProviderType != "" && file.ProviderType != f.ProviderType {
		return false
	}
	if f.Purpose != "" && file.Purpose != f.Purpose {
		return false
	}
	return true
}

// listLimit bounds a page the way the public files API does.
func listLimit(limit int) int {
	switch {
	case limit <= 0:
		return 20
	case limit > 101:
		return 101
	default:
		return limit
	}
}

// newerThan orders mappings newest first with the id as tie breaker.
func newerThan(a, b *StoredFile) bool {
	if a.CreatedAt == b.CreatedAt {
		return a.ID > b.ID
	}
	return a.CreatedAt > b.CreatedAt
}

func scanStoredFile(row sqlx.Row) (*StoredFile, error) {
	file := &StoredFile{}
	err := row.Scan(&file.ID, &file.ProviderType, &file.Purpose, &file.Filename, &file.Bytes, &file.CreatedAt, &file.UserPath)
	if err != nil {
		if errors.Is(err, sqlx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query file mapping: %w", err)
	}
	return cloneStoredFile(file)
}

func normalizeStoredFile(file *StoredFile) (*StoredFile, error) {
	if file == nil {
		return nil, fmt.Errorf("file mapping is nil")
	}
	normalized := *file
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.ProviderType = strings.TrimSpace(normalized.ProviderType)
	normalized.Purpose = strings.TrimSpace(normalized.Purpose)
	normalized.Filename = strings.TrimSpace(normalized.Filename)
	normalized.UserPath = strings.TrimSpace(normalized.UserPath)
	if normalized.ID == "" {
		return nil, fmt.Errorf("file id is required")
	}
	if normalized.ProviderType == "" {
		return nil, fmt.Errorf("provider type is required")
	}
	return &normalized, nil
}

func cloneStoredFile(file *StoredFile) (*StoredFile, error) {
	normalized, err := normalizeStoredFile(file)
	if err != nil {
		return nil, err
	}
	cloned := *normalized
	return &cloned, nil
}
