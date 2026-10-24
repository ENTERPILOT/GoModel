//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Opening a FIFO with no writer blocks in the kernel, where no context
// reaches it, and a pipe cannot be re-read on reload or rotation anyway.
func TestResolveFileSecretRefusesAFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "key.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	t.Cleanup(func() {
		// Unblocks a reader stuck in open, if there is one.
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := NewSecrets().Resolve(t.Context(), "${file:"+fifo+"}")
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a FIFO blocked")
	}
}

// Kubernetes mounts each secret key as a symlink through ..data into a
// timestamped directory of regular files.
func TestResolveFileSecretFollowsKubernetesSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "..2026_10_09"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "..2026_10_09", "api-key"), []byte("sk-mounted\n"), 0o600))
	require.NoError(t, os.Symlink("..2026_10_09", filepath.Join(dir, "..data")))
	require.NoError(t, os.Symlink(filepath.Join("..data", "api-key"), filepath.Join(dir, "api-key")))

	value, err := NewSecrets().Resolve(t.Context(), "${file:"+filepath.Join(dir, "api-key")+"}")
	require.NoError(t, err)
	require.Equal(t, "sk-mounted", value)
}
