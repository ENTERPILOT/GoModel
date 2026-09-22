package blobstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runStoreSuite exercises the behaviour every Store owes its callers against
// each backend.
func runStoreSuite(t *testing.T, suite func(t *testing.T, store Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		suite(t, NewMemory())
	})
	t.Run("filesystem", func(t *testing.T) {
		store, err := NewFilesystem(filepath.Join(t.TempDir(), "media"))
		require.NoError(t, err)
		suite(t, store)
	})
}

func readAll(t *testing.T, store Store, key string) string {
	t.Helper()
	r, err := store.Open(context.Background(), key)
	require.NoError(t, err)
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func TestStore_PutOpenDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		n, err := Put(ctx, store, "audio/2026/09/22/a.mp3", strings.NewReader("hello"))
		require.NoError(t, err)
		assert.Equal(t, int64(5), n)
		assert.Equal(t, "hello", readAll(t, store, "audio/2026/09/22/a.mp3"))

		// Readers seek, which range requests rely on.
		r, err := store.Open(ctx, "audio/2026/09/22/a.mp3")
		require.NoError(t, err)
		_, err = r.Seek(2, io.SeekStart)
		require.NoError(t, err)
		rest, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		assert.Equal(t, "llo", string(rest))

		require.NoError(t, store.Delete(ctx, "audio/2026/09/22/a.mp3"))
		_, err = store.Open(ctx, "audio/2026/09/22/a.mp3")
		require.ErrorIs(t, err, ErrNotFound)
		assert.NoError(t, store.Delete(ctx, "audio/2026/09/22/a.mp3"), "deleting a missing blob is not an error")
	})
}

func TestStore_CloseWithoutCommitDiscards(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		w, err := store.Create(ctx, "image/x.png")
		require.NoError(t, err)
		_, err = w.Write([]byte("partial"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		_, err = store.Open(ctx, "image/x.png")
		require.ErrorIs(t, err, ErrNotFound)
		_, err = w.Write([]byte("more"))
		require.Error(t, err, "a closed writer refuses writes")
		assert.Error(t, w.Commit(), "a closed writer refuses commit")
	})
}

func TestStore_CommitReplacesAndIsIdempotent(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		_, err := Put(ctx, store, "k", strings.NewReader("one"))
		require.NoError(t, err)

		w, err := store.Create(ctx, "k")
		require.NoError(t, err)
		_, err = w.Write([]byte("two"))
		require.NoError(t, err)
		require.NoError(t, w.Commit())
		assert.NoError(t, w.Commit(), "second commit is a no-op")
		assert.NoError(t, w.Close(), "close after commit is a no-op")
		assert.Equal(t, "two", readAll(t, store, "k"))
	})
}

func TestStore_RejectsUnsafeKeys(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		for _, key := range []string{"", "/abs", "a//b", "../escape", "a/../b", ".", "with space", "semi;colon"} {
			_, err := store.Create(ctx, key)
			require.Error(t, err, "Create(%q)", key)
			_, err = store.Open(ctx, key)
			require.Error(t, err, "Open(%q)", key)
			assert.Error(t, store.Delete(ctx, key), "Delete(%q)", key)
		}
	})
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		key string
		ok  bool
	}{
		{"a", true},
		{"audio/2026/09/22/med_ab12.mp3", true},
		{"A-Z_0.9", true},
		{"", false},
		{"/a", false},
		{"a/", false},
		{"a/../b", false},
		{"a/./b", false},
		{"a b", false},
		{"ünïcode", false},
		{"a\\b", false},
	}
	for _, tt := range tests {
		err := ValidateKey(tt.key)
		if tt.ok {
			assert.NoError(t, err, "ValidateKey(%q)", tt.key)
		} else {
			assert.Error(t, err, "ValidateKey(%q)", tt.key)
		}
	}
}

func TestFilesystem_LayoutAndCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	store, err := NewFilesystem(root)
	require.NoError(t, err)
	ctx := context.Background()

	_, err = Put(ctx, store, "audio/2026/09/22/a.mp3", strings.NewReader("x"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "audio", "2026", "09", "22", "a.mp3"))
	require.NoError(t, err, "blob lands at the path its key spells")

	entries, err := os.ReadDir(filepath.Join(root, "audio", "2026", "09", "22"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file left behind after commit")

	// An abandoned writer leaves no temp file either.
	w, err := store.Create(ctx, "audio/2026/09/22/b.mp3")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	entries, err = os.ReadDir(filepath.Join(root, "audio", "2026", "09", "22"))
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	require.NoError(t, store.Delete(ctx, "audio/2026/09/22/a.mp3"))
	_, err = os.Stat(filepath.Join(root, "audio"))
	assert.True(t, os.IsNotExist(err), "empty date directories are pruned after delete")
	_, err = os.Stat(root)
	assert.NoError(t, err, "the root itself stays")
}

func TestFilesystem_OpenDirectoryIsNotFound(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystem(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "audio"), 0o755))
	_, err = store.Open(context.Background(), "audio")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestNewFilesystem_RequiresRoot(t *testing.T) {
	_, err := NewFilesystem("  ")
	assert.Error(t, err)
}

func TestFilesystem_SyncFailureUnpublishesTheBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	store, err := NewFilesystem(root)
	require.NoError(t, err)
	original := syncDir
	syncDir = func(string) error { return assert.AnError }
	t.Cleanup(func() { syncDir = original })

	w, err := store.Create(context.Background(), "audio/2026/09/22/a.mp3")
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.ErrorIs(t, w.Commit(), assert.AnError)
	require.NoError(t, w.Close())

	_, err = store.Open(context.Background(), "audio/2026/09/22/a.mp3")
	require.ErrorIs(t, err, ErrNotFound, "a blob whose publish did not complete must not stay on disk")
	entries, err := os.ReadDir(filepath.Join(root, "audio", "2026", "09", "22"))
	require.NoError(t, err)
	assert.Empty(t, entries, "no temp file or renamed file left behind")
}
