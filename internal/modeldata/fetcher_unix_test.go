//go:build unix

package modeldata

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFetchIfChanged_LocalFIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "catalog.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Opening a FIFO with no writer blocks forever; readLocal must reject it
	// from metadata instead of hanging startup.
	done := make(chan error, 1)
	go func() {
		_, err := FetchIfChanged(context.Background(), fifo, "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("expected not-a-regular-file error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FetchIfChanged blocked on a FIFO")
	}
}
