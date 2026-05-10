// Package filestore persists provider ownership for uploaded files.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	Delete(ctx context.Context, id string) error
	Close() error
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanStoredFile(row rowScanner, notFound error) (*StoredFile, error) {
	file := &StoredFile{}
	err := row.Scan(&file.ID, &file.ProviderType, &file.Purpose, &file.Filename, &file.Bytes, &file.CreatedAt, &file.UserPath)
	if err != nil {
		if errors.Is(err, notFound) {
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
