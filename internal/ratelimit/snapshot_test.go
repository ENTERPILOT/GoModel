package ratelimit

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childLimiter returns a limiter holding one counted, already-snapshotted
// counter per child of a per-child template, and the children's rules.
func childLimiter(tb testing.TB, children int, now time.Time) (*limiter, Rule, []Rule) {
	tb.Helper()
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1 << 40)), MaxTokens: new(int64(1 << 40)),
	}
	l := newLimiter()
	tb.Cleanup(l.close)
	rules := make([]Rule, children)
	for i := range rules {
		child, ok := template.resolve(Subjects{UserPath: fmt.Sprintf("/customers/u%d", i)})
		require.True(tb, ok)
		rules[i] = child
		_, _, err := l.admit([]Rule{child}, now)
		require.Nil(tb, err)
	}
	require.Len(tb, l.snapshot([]Rule{template}), children)
	return l, template, rules
}

func TestSnapshotReturnsOnlyChangedCounters(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	l, template, children := childLimiter(t, 10_000, now)

	_, _, err := l.admit([]Rule{children[7]}, now)
	require.Nil(t, err)
	l.recordTokens([]Rule{children[4242]}, 5, now)
	_, _, err = l.admit([]Rule{children[9999]}, now)
	require.Nil(t, err)
	l.recordTokens([]Rule{children[9999]}, 3, now)

	snaps := l.snapshot([]Rule{template})
	partitions := make([]string, 0, len(snaps))
	for _, snap := range snaps {
		partitions = append(partitions, snap.Partition)
	}
	slices.Sort(partitions)
	assert.Equal(t, []string{"/customers/u4242", "/customers/u7", "/customers/u9999"}, partitions)
	for _, snap := range snaps {
		switch snap.Partition {
		case "/customers/u7":
			assert.Equal(t, int64(2), snap.RequestsCurrent)
			assert.Zero(t, snap.TokensCurrent)
		case "/customers/u4242":
			assert.Equal(t, int64(1), snap.RequestsCurrent)
			assert.Equal(t, int64(5), snap.TokensCurrent)
		case "/customers/u9999":
			assert.Equal(t, int64(2), snap.RequestsCurrent)
			assert.Equal(t, int64(3), snap.TokensCurrent)
		}
	}
	assert.Empty(t, l.snapshot([]Rule{template}))
}

func TestSnapshotSkipsCountersRemovedSinceTheirChange(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(10)),
	}
	alice, _ := template.resolve(Subjects{UserPath: "/customers/alice"})
	bob, _ := template.resolve(Subjects{UserPath: "/customers/bob"})

	tests := []struct {
		name          string
		snapshotFirst bool
		remove        func(l *limiter)
		want          []string
	}{
		{name: "reset", remove: func(l *limiter) { l.reset(keyForRule(alice)) }},
		{name: "reset all", remove: func(l *limiter) { l.resetAll() }},
		{name: "expired", remove: func(l *limiter) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.pruneCounterExpiries(time.Now().Unix() + 3*PeriodHourSeconds)
		}},
		{name: "reset then counted again", snapshotFirst: true, remove: func(l *limiter) {
			l.reset(keyForRule(alice))
			_, _, err := l.admit([]Rule{bob}, now)
			assert.Nil(t, err)
		}, want: []string{"/customers/bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLimiter()
			t.Cleanup(l.close)
			_, _, err := l.admit([]Rule{alice}, now)
			require.Nil(t, err)
			_, _, err = l.admit([]Rule{bob}, now)
			require.Nil(t, err)
			if tt.snapshotFirst {
				require.Len(t, l.snapshot([]Rule{template}), 2)
			}

			tt.remove(l)

			var got []string
			for _, snap := range l.snapshot([]Rule{template}) {
				got = append(got, snap.Partition)
			}
			assert.Equal(t, tt.want, got)
			l.mu.Lock()
			assert.Empty(t, l.dirty)
			l.mu.Unlock()
		})
	}
}

// BenchmarkAdmitDuringSnapshot runs one flush's snapshot over 100k idle
// per-child counters while eight goroutines each admit a few requests, and
// reports the admits' p99 latency: how long admission stalls behind the
// snapshot holding the mutex.
func BenchmarkAdmitDuringSnapshot(b *testing.B) {
	now := time.Unix(1_700_000_000, 0).UTC()
	l, template, children := childLimiter(b, 100_000, now)
	rules := []Rule{template}

	const workers, admits = 8, 16
	latencies := make([][]time.Duration, workers)
	for b.Loop() {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			l.snapshot(rules)
		})
		for w := range workers {
			wg.Go(func() {
				admit := []Rule{children[w]}
				<-start
				for range admits {
					began := time.Now()
					_, _, _ = l.admit(admit, now)
					latencies[w] = append(latencies[w], time.Since(began))
				}
			})
		}
		close(start)
		wg.Wait()
	}
	b.StopTimer()

	var all []time.Duration
	for _, worker := range latencies {
		all = append(all, worker...)
	}
	if len(all) == 0 {
		return
	}
	slices.Sort(all)
	b.ReportMetric(float64(all[len(all)*99/100].Nanoseconds()), "p99-admit-ns")
}
