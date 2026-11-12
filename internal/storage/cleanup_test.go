package storage

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstSweepDelay(t *testing.T) {
	lowest := func(int64) int64 { return 0 }
	highest := func(n int64) int64 { return n - 1 }
	tests := []struct {
		name     string
		interval time.Duration
		randN    func(int64) int64
		want     time.Duration
	}{
		{"hourly, earliest", time.Hour, lowest, FirstSweepMinDelay},
		{"hourly, latest", time.Hour, highest, FirstSweepMinDelay + FirstSweepJitter - 1},
		{"short interval, earliest", time.Minute, lowest, 0},
		{"short interval, latest", time.Minute, highest, time.Minute - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, firstSweepDelay(tt.interval, tt.randN))
		})
	}
}

func TestRunCleanupLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		runs := make(chan time.Duration, 8)
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			RunCleanupLoop(stop, time.Hour, func() { runs <- time.Since(start) })
		}()

		synctest.Wait()
		assert.Empty(t, runs, "no sweep at startup")

		time.Sleep(FirstSweepMinDelay + FirstSweepJitter + 2*time.Hour)
		synctest.Wait()
		close(stop)
		<-done
		close(runs)
		var got []time.Duration
		for run := range runs {
			got = append(got, run)
		}
		require.Len(t, got, 3)
		assert.GreaterOrEqual(t, got[0], FirstSweepMinDelay)
		assert.Less(t, got[0], FirstSweepMinDelay+FirstSweepJitter)
		assert.Equal(t, time.Hour, got[1]-got[0])
		assert.Equal(t, time.Hour, got[2]-got[1])
	})
}
