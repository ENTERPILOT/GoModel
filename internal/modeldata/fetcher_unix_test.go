//go:build unix

package modeldata

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchIfChanged_LocalFIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "catalog.fifo")
	err := syscall.Mkfifo(fifo, 0o600)
	require.NoError(t, err)

	// Opening a FIFO with no writer blocks forever; readLocal must reject it
	// from metadata instead of hanging startup.
	done := make(chan error, 1)
	go func() {
		_, err := FetchIfChanged(context.Background(), fifo, "")
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")

	case <-time.After(5 * time.Second):
		t.Fatal("FetchIfChanged blocked on a FIFO")
	}
}
