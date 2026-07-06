package storage

import (
	"testing"
	"time"
)

func TestRunCleanupLoop(t *testing.T) {
	calls := make(chan struct{}, 64)
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		RunCleanupLoop(stop, 5*time.Millisecond, func() { calls <- struct{}{} })
		close(done)
	}()

	// The first cleanup runs immediately, then once per tick.
	for i, phase := range []string{"initial cleanup", "first tick", "second tick"} {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("phase %d (%s): cleanup was not invoked", i, phase)
		}
	}

	// Closing stop makes the loop return; once the goroutine has exited, no
	// further cleanups can run by construction, so exit is the property to
	// assert (ticks racing the stop signal may legitimately land in calls).
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after stop was closed")
	}
}
