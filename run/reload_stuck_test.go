package run

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reload stuck building its replacement, for example on a secret resolver
// that never returns, must not keep the process from exiting on shutdown.
func TestServeUntilShutdownDoesNotWaitForAStuckReload(t *testing.T) {
	previousTimeout := shutdownTimeout
	shutdownTimeout = 50 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = previousTimeout })

	socket := testSocket(t)
	first := newFakeGeneration()
	late := newFakeGeneration()
	building := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
	rebuild := func() (lifecycleApp, error) {
		close(building)
		<-released
		return late, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reload := make(chan os.Signal, 1)
	served := make(chan error, 1)
	go func() { served <- serveUntilShutdown(ctx, reload, socket, first, rebuild) }()

	<-first.started
	reload <- reloadSignal
	select {
	case <-building:
	case <-time.After(5 * time.Second):
		t.Fatal("the reload never started")
	}

	cancel()
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited for a reload that never finishes")
	}
	assert.Equal(t, int32(1), first.shutdowns.Load())

	// A replacement that is built after all is torn down, never served.
	release()
	require.Eventually(t, func() bool { return late.shutdowns.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	select {
	case <-late.started:
		t.Error("the abandoned replacement was served")
	default:
	}
}
