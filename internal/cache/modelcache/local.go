package modelcache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LocalCache implements Cache using local file storage.
// This is suitable for single-instance deployments.
type LocalCache struct {
	mu       sync.RWMutex
	filePath string
}

// NewLocalCache creates a new local file-based cache.
// The filePath specifies where the cache file will be stored.
func NewLocalCache(filePath string) *LocalCache {
	return &LocalCache{
		filePath: filePath,
	}
}

// Get retrieves the model cache from the local file.
func (c *LocalCache) Get(ctx context.Context) (*ModelCache, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.filePath == "" {
		return nil, nil
	}

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var cache ModelCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("failed to parse cache file: %w", err)
	}

	return &cache, nil
}

// Set stores the model cache to the local file.
func (c *LocalCache) Set(ctx context.Context, cache *ModelCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.filePath == "" {
		return nil
	}

	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// Write atomically using temp file + rename
	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}
	if err := os.Rename(tmpFile, c.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	return nil
}

// Close is a no-op for local cache.
func (c *LocalCache) Close() error {
	return nil
}
