package storage

import "time"

// RunCleanupLoop runs a cleanup function immediately and then at the given
// interval until the stop channel is closed. Store backends use it for
// periodic retention sweeps.
func RunCleanupLoop(stop <-chan struct{}, interval time.Duration, cleanupFn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	cleanupFn()

	for {
		select {
		case <-ticker.C:
			cleanupFn()
		case <-stop:
			return
		}
	}
}
