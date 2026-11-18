package server

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/blobstore"
	"github.com/enterpilot/gomodel/internal/mediastore"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// newTestMediaCapturer returns a request log media capturer backed by in-memory
// stores, plus the service so a test can read back what was stored.
func newTestMediaCapturer(t *testing.T) (*requestlog.MediaCapturer, *mediastore.Service) {
	t.Helper()
	store := mediastore.NewService(mediastore.NewMemoryStore(), blobstore.NewMemory())
	t.Cleanup(func() { _ = store.Close() })
	return requestlog.NewMediaCapturer(store, 30), store
}

// readTestMedia returns the bytes stored under a media id.
func readTestMedia(t *testing.T, store *mediastore.Service, id string) []byte {
	t.Helper()
	require.NotEmpty(t, id, "media id")
	_, reader, err := store.Open(context.Background(), id)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return data
}
