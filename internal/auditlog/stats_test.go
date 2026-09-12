package auditlog

import (
	"context"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

func hourRow(hour time.Time, provider string, mutate func(*statsRow)) statsRow {
	row := statsRow{HourUTC: hour, Provider: provider}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

func TestFoldRequestStats_HourInterval(t *testing.T) {
	day := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	rows := []statsRow{
		hourRow(day.Add(10*time.Hour), "openai-prod", func(r *statsRow) {
			r.Requests = 3
			r.Status2xx = 2
			r.Status4xx = 1
			r.DurationNsSum = 400e6
			r.DurationCount = 2
		}),
		hourRow(day.Add(10*time.Hour), "anthropic", func(r *statsRow) {
			r.Requests = 1
			r.Status2xx = 1
			r.DurationNsSum = 900e6
			r.DurationCount = 1
		}),
		hourRow(day.Add(11*time.Hour), "openai-prod", func(r *statsRow) {
			r.Requests = 2
			r.Status5xx = 1
			r.Status2xx = 0
			// One request never resolved a status (recorded as 0) -> other.
			r.Status4xx = 0
		}),
	}

	stats := foldRequestStats(rows, RequestStatsParams{
		StartDate: day, EndDate: day,
		Interval: StatsIntervalHour,
		Location: time.UTC,
		Now:      day.Add(12*time.Hour + 30*time.Minute),
	})

	require.Equal(t, StatsIntervalHour, stats.Interval)

	// Zero-filled from local midnight through the bucket containing Now.
	require.Len(t, stats.Buckets, 13)
	require.True(t, stats.Buckets[0].Start.Equal(day), "first bucket = %v, want %v", stats.Buckets[0].Start, day)

	ten := stats.Buckets[10]
	require.Equal(t, int64(4), ten.Requests)
	require.Equal(t, int64(3), ten.Status2xx)
	require.Equal(t, int64(1), ten.Status4xx)
	require.Equal(t, int64(0), ten.Status5xx)
	require.Equal(t, int64(0), ten.StatusOther, "10:00 bucket = %+v", ten)

	eleven := stats.Buckets[11]
	require.Equal(t, int64(2), eleven.Requests)
	require.Equal(t, int64(1), eleven.Status5xx)
	require.Equal(t, int64(1), eleven.StatusOther, "11:00 bucket = %+v", eleven)
	require.Equal(t, int64(6), stats.Summary.Requests)
	require.Equal(t, int64(3), stats.Summary.Status2xx)
	require.Equal(t, int64(1), stats.Summary.StatusOther, "summary = %+v", stats.Summary)
	require.NotNil(t, stats.Summary.SuccessRate)
	require.Equal(t, float64(3)/float64(6), *stats.Summary.SuccessRate)

	wantAvg := float64(400e6+900e6) / 3 / 1e6
	require.NotNil(t, stats.Summary.AvgDurationMs)
	require.InDelta(t, wantAvg, *stats.Summary.AvgDurationMs, 1e-9)
	require.Len(t, stats.ProviderLatency, 2)

	// Busiest provider (by latency-eligible requests) first.
	require.Equal(t, "openai-prod", stats.ProviderLatency[0].Provider)
	require.Equal(t, "anthropic", stats.ProviderLatency[1].Provider)

	openai := stats.ProviderLatency[0]
	require.Len(t, openai.AvgDurationMs, len(stats.Buckets))
	require.NotNil(t, openai.AvgDurationMs[10])
	require.Equal(t, float64(200), *openai.AvgDurationMs[10])
	require.Equal(t, int64(2), openai.Requests[10])

	// The 11:00 failure bucket has no eligible requests -> a gap, not zero.
	require.Nil(t, openai.AvgDurationMs[11])
}

func TestFoldRequestStats_DayIntervalFoldsHoursIntoLocalDays(t *testing.T) {
	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)

	// 23:30 UTC on Jan 16 is already Jan 17 00:30 in Warsaw (UTC+1).
	rows := []statsRow{
		hourRow(time.Date(2026, 1, 16, 23, 0, 0, 0, time.UTC), "openai", func(r *statsRow) {
			r.Requests = 1
			r.Status2xx = 1
		}),
		hourRow(time.Date(2026, 1, 17, 10, 0, 0, 0, time.UTC), "openai", func(r *statsRow) {
			r.Requests = 2
			r.Status2xx = 2
		}),
		hourRow(time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC), "openai", func(r *statsRow) {
			r.Requests = 4
			r.Status2xx = 4
		}),
	}

	start := time.Date(2026, 1, 16, 0, 0, 0, 0, location)
	end := time.Date(2026, 1, 17, 0, 0, 0, 0, location)
	stats := foldRequestStats(rows, RequestStatsParams{
		StartDate: start, EndDate: end,
		Interval: StatsIntervalDay,
		Location: location,
		Now:      time.Date(2026, 1, 18, 12, 0, 0, 0, location),
	})

	require.Len(t, stats.Buckets, 2)
	require.True(t, stats.Buckets[0].Start.Equal(start))
	require.True(t, stats.Buckets[1].Start.Equal(end), "bucket starts = %v, %v", stats.Buckets[0].Start, stats.Buckets[1].Start)
	require.Equal(t, int64(4), stats.Buckets[0].Requests)
	require.Equal(t, int64(3), stats.Buckets[1].Requests)
}

func TestFoldRequestStats_ZeroFillStopsAtNow(t *testing.T) {
	location := time.UTC
	start := time.Date(2026, 1, 10, 0, 0, 0, 0, location)
	end := time.Date(2026, 1, 20, 0, 0, 0, 0, location)

	stats := foldRequestStats(nil, RequestStatsParams{
		StartDate: start, EndDate: end,
		Interval: StatsIntervalDay,
		Location: location,
		Now:      time.Date(2026, 1, 12, 15, 0, 0, 0, location),
	})

	require.Len(t, stats.Buckets, 3)
	require.Nil(t, stats.Summary.SuccessRate)
	require.Nil(t, stats.Summary.AvgDurationMs, "empty summary rates = %+v, want nil", stats.Summary)
	require.Empty(t, stats.ProviderLatency)
}

func TestSQLReaderGetRequestStats(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		day := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
		entries := []*LogEntry{
			{ID: "ok-1", Timestamp: day.Add(10*time.Hour + 15*time.Minute), Provider: "openai", ProviderName: "openai-prod", StatusCode: 200, DurationNs: 100e6},
			{ID: "ok-2", Timestamp: day.Add(10*time.Hour + 45*time.Minute), Provider: "openai", ProviderName: "openai-prod", StatusCode: 201, DurationNs: 300e6},
			{ID: "client-err", Timestamp: day.Add(10*time.Hour + 50*time.Minute), Provider: "openai", ProviderName: "openai-prod", StatusCode: 429, DurationNs: 5e6},
			{ID: "server-err", Timestamp: day.Add(11*time.Hour + 5*time.Minute), Provider: "openai", ProviderName: "openai-prod", StatusCode: 502, DurationNs: 2e9},
			// Local cache hit: counted as 2xx but excluded from latency.
			{ID: "cache-hit", Timestamp: day.Add(11*time.Hour + 10*time.Minute), Provider: "openai", ProviderName: "openai-prod", StatusCode: 200, DurationNs: 1e6, CacheType: CacheTypeExact},
			// Empty provider name falls back to the provider type.
			{ID: "fallback-name", Timestamp: day.Add(11*time.Hour + 20*time.Minute), Provider: "anthropic", StatusCode: 200, DurationNs: 700e6},
			// Outside the queried range.
			{ID: "next-day", Timestamp: day.Add(30 * time.Hour), Provider: "openai", ProviderName: "openai-prod", StatusCode: 200, DurationNs: 100e6},
		}
		err = store.WriteBatch(context.Background(), entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		stats, err := reader.GetRequestStats(context.Background(), RequestStatsParams{
			StartDate: day, EndDate: day,
			Interval: StatsIntervalHour,
			Location: time.UTC,
			Now:      day.Add(23 * time.Hour),
		})
		require.NoError(t, err)
		require.Equal(t, int64(6), stats.Summary.Requests)
		require.Equal(t, int64(4), stats.Summary.Status2xx)
		require.Equal(t, int64(1), stats.Summary.Status4xx)
		require.Equal(t, int64(1), stats.Summary.Status5xx)
		require.Equal(t, int64(0), stats.Summary.StatusOther, "summary = %+v", stats.Summary)

		byStart := map[int]RequestStatsBucket{}
		for _, b := range stats.Buckets {
			byStart[b.Start.UTC().Hour()] = b
		}
		b := byStart[10]
		require.Equal(t, int64(3), b.Requests)
		require.Equal(t, int64(2), b.Status2xx)
		require.Equal(t, int64(1), b.Status4xx, "10:00 bucket = %+v", b)
		b = byStart[11]
		require.Equal(t, int64(3), b.Requests)
		require.Equal(t, int64(2), b.Status2xx)
		require.Equal(t, int64(1), b.Status5xx, "11:00 bucket = %+v", b)
		require.Len(t, stats.ProviderLatency, 2)
		require.Equal(t, "openai-prod", stats.ProviderLatency[0].Provider)

		openai := stats.ProviderLatency[0]
		require.NotNil(t, openai.AvgDurationMs[10])
		require.Equal(t, float64(200), *openai.AvgDurationMs[10])

		// The 11:00 openai bucket only saw a 502 and a cache hit -> gap.
		require.Nil(t, openai.AvgDurationMs[11])

		anthropic := stats.ProviderLatency[1]
		require.Equal(t, "anthropic", anthropic.Provider)
		require.NotNil(t, anthropic.AvgDurationMs[11])
		require.Equal(t, float64(700), *anthropic.AvgDurationMs[11])
	})
}

func TestSQLReaderGetRequestStats_FiltersByUserPathSubtree(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := newSQLStoreForTest(t, db, 0)
		require.NoError(t, err)

		day := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
		entries := []*LogEntry{
			{ID: "alpha", Timestamp: day.Add(10 * time.Hour), Provider: "openai", StatusCode: 200, DurationNs: 100e6, UserPath: "/team/alpha"},
			{ID: "alpha-child", Timestamp: day.Add(10 * time.Hour), Provider: "openai", StatusCode: 500, DurationNs: 100e6, UserPath: "/team/alpha/service"},
			{ID: "alpha-sibling", Timestamp: day.Add(10 * time.Hour), Provider: "openai", StatusCode: 200, DurationNs: 100e6, UserPath: "/team/alpha-2"},
			{ID: "beta", Timestamp: day.Add(10 * time.Hour), Provider: "openai", StatusCode: 200, DurationNs: 100e6, UserPath: "/team/beta"},
			{ID: "no-path", Timestamp: day.Add(10 * time.Hour), Provider: "openai", StatusCode: 200, DurationNs: 100e6},
		}
		err = store.WriteBatch(context.Background(), entries)
		require.NoError(t, err)

		reader, err := NewSQLReader(db)
		require.NoError(t, err)

		tests := []struct {
			name         string
			userPath     string
			wantRequests int64
			wantStatus5x int64
		}{
			{name: "no filter counts everything", wantRequests: 5, wantStatus5x: 1},
			{name: "subtree counts path and descendants", userPath: "/team/alpha", wantRequests: 2, wantStatus5x: 1},
			{name: "root counts every tracked path", userPath: "/", wantRequests: 5, wantStatus5x: 1},
			{name: "unknown subtree is empty", userPath: "/team/gamma", wantRequests: 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				stats, err := reader.GetRequestStats(context.Background(), RequestStatsParams{
					StartDate: day, EndDate: day,
					UserPath: tt.userPath,
					Interval: StatsIntervalHour,
					Location: time.UTC,
					Now:      day.Add(23 * time.Hour),
				})
				require.NoError(t, err)
				require.Equal(t, tt.wantRequests, stats.Summary.Requests)
				require.Equal(t, tt.wantStatus5x, stats.Summary.Status5xx)
			})
		}
	})
}
