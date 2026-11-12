package storage

import (
	"math/rand/v2"
	"time"
)

// The first sweep runs a random FirstSweepMinDelay to
// FirstSweepMinDelay+FirstSweepJitter after startup rather than at boot. The
// delay keeps the sweep out of the startup path, where a long DELETE would
// hold up the schema changes of the next replica in a rolling restart, and
// the jitter spreads replicas started together onto different hourly phases.
// A few minutes is short enough that a single node still sweeps soon after
// it starts.
const (
	FirstSweepMinDelay = time.Minute
	FirstSweepJitter   = 4 * time.Minute
)

// RunCleanupLoop runs a cleanup function shortly after startup (see
// FirstSweepMinDelay) and then at the given interval until the stop channel
// is closed. Store backends use it for periodic retention sweeps. The
// interval must be positive.
func RunCleanupLoop(stop <-chan struct{}, interval time.Duration, cleanupFn func()) {
	runCleanupLoop(stop, firstSweepDelay(interval, rand.Int64N), interval, cleanupFn)
}

func runCleanupLoop(stop <-chan struct{}, first, interval time.Duration, cleanupFn func()) {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			cleanupFn()
			timer.Reset(interval)
		case <-stop:
			return
		}
	}
}

// firstSweepDelay picks the delay before the first sweep. An interval
// shorter than the usual delay bounds it, so the first sweep is never later
// than a regular one would be. randN returns a value in [0, n).
func firstSweepDelay(interval time.Duration, randN func(n int64) int64) time.Duration {
	if interval <= FirstSweepMinDelay+FirstSweepJitter {
		return time.Duration(randN(int64(interval)))
	}
	return FirstSweepMinDelay + time.Duration(randN(int64(FirstSweepJitter)))
}
