package auditlog

import (
	"context"
	"math"
	"testing"
	"time"
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
		QueryParams: QueryParams{StartDate: day, EndDate: day},
		Interval:    StatsIntervalHour,
		Location:    time.UTC,
		Now:         day.Add(12*time.Hour + 30*time.Minute),
	})

	if stats.Interval != StatsIntervalHour {
		t.Fatalf("interval = %q, want hour", stats.Interval)
	}
	// Zero-filled from local midnight through the bucket containing Now.
	if len(stats.Buckets) != 13 {
		t.Fatalf("bucket count = %d, want 13", len(stats.Buckets))
	}
	if !stats.Buckets[0].Start.Equal(day) {
		t.Fatalf("first bucket = %v, want %v", stats.Buckets[0].Start, day)
	}

	ten := stats.Buckets[10]
	if ten.Requests != 4 || ten.Status2xx != 3 || ten.Status4xx != 1 || ten.Status5xx != 0 || ten.StatusOther != 0 {
		t.Fatalf("10:00 bucket = %+v", ten)
	}
	eleven := stats.Buckets[11]
	if eleven.Requests != 2 || eleven.Status5xx != 1 || eleven.StatusOther != 1 {
		t.Fatalf("11:00 bucket = %+v", eleven)
	}

	if stats.Summary.Requests != 6 || stats.Summary.Status2xx != 3 || stats.Summary.StatusOther != 1 {
		t.Fatalf("summary = %+v", stats.Summary)
	}
	if stats.Summary.SuccessRate == nil || *stats.Summary.SuccessRate != float64(3)/float64(6) {
		t.Fatalf("success rate = %v", stats.Summary.SuccessRate)
	}
	wantAvg := float64(400e6+900e6) / 3 / 1e6
	if stats.Summary.AvgDurationMs == nil || math.Abs(*stats.Summary.AvgDurationMs-wantAvg) > 1e-9 {
		t.Fatalf("avg duration = %v, want %v", stats.Summary.AvgDurationMs, wantAvg)
	}

	if len(stats.ProviderLatency) != 2 {
		t.Fatalf("provider series = %d, want 2", len(stats.ProviderLatency))
	}
	// Busiest provider (by latency-eligible requests) first.
	if stats.ProviderLatency[0].Provider != "openai-prod" || stats.ProviderLatency[1].Provider != "anthropic" {
		t.Fatalf("provider order = %q, %q", stats.ProviderLatency[0].Provider, stats.ProviderLatency[1].Provider)
	}
	openai := stats.ProviderLatency[0]
	if len(openai.AvgDurationMs) != len(stats.Buckets) {
		t.Fatalf("series length = %d, want %d", len(openai.AvgDurationMs), len(stats.Buckets))
	}
	if openai.AvgDurationMs[10] == nil || *openai.AvgDurationMs[10] != 200 {
		t.Fatalf("openai 10:00 avg = %v, want 200", openai.AvgDurationMs[10])
	}
	if openai.Requests[10] != 2 {
		t.Fatalf("openai 10:00 requests = %d, want 2", openai.Requests[10])
	}
	// The 11:00 failure bucket has no eligible requests -> a gap, not zero.
	if openai.AvgDurationMs[11] != nil {
		t.Fatalf("openai 11:00 avg = %v, want nil gap", openai.AvgDurationMs[11])
	}
}

func TestFoldRequestStats_DayIntervalFoldsHoursIntoLocalDays(t *testing.T) {
	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

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
		QueryParams: QueryParams{StartDate: start, EndDate: end},
		Interval:    StatsIntervalDay,
		Location:    location,
		Now:         time.Date(2026, 1, 18, 12, 0, 0, 0, location),
	})

	if len(stats.Buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(stats.Buckets))
	}
	if !stats.Buckets[0].Start.Equal(start) || !stats.Buckets[1].Start.Equal(end) {
		t.Fatalf("bucket starts = %v, %v", stats.Buckets[0].Start, stats.Buckets[1].Start)
	}
	if stats.Buckets[0].Requests != 4 {
		t.Fatalf("Jan 16 requests = %d, want 4", stats.Buckets[0].Requests)
	}
	if stats.Buckets[1].Requests != 3 {
		t.Fatalf("Jan 17 requests = %d, want 3 (late UTC hour folds into the next local day)", stats.Buckets[1].Requests)
	}
}

func TestFoldRequestStats_ZeroFillStopsAtNow(t *testing.T) {
	location := time.UTC
	start := time.Date(2026, 1, 10, 0, 0, 0, 0, location)
	end := time.Date(2026, 1, 20, 0, 0, 0, 0, location)

	stats := foldRequestStats(nil, RequestStatsParams{
		QueryParams: QueryParams{StartDate: start, EndDate: end},
		Interval:    StatsIntervalDay,
		Location:    location,
		Now:         time.Date(2026, 1, 12, 15, 0, 0, 0, location),
	})

	if len(stats.Buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3 (10th-12th)", len(stats.Buckets))
	}
	if stats.Summary.SuccessRate != nil || stats.Summary.AvgDurationMs != nil {
		t.Fatalf("empty summary rates = %+v, want nil", stats.Summary)
	}
	if len(stats.ProviderLatency) != 0 {
		t.Fatalf("provider series = %d, want 0", len(stats.ProviderLatency))
	}
}

func TestSQLiteReaderGetRequestStats(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

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
	if err := store.WriteBatch(context.Background(), entries); err != nil {
		t.Fatalf("failed to seed audit logs: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	stats, err := reader.GetRequestStats(context.Background(), RequestStatsParams{
		QueryParams: QueryParams{StartDate: day, EndDate: day},
		Interval:    StatsIntervalHour,
		Location:    time.UTC,
		Now:         day.Add(23 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetRequestStats failed: %v", err)
	}

	if stats.Summary.Requests != 6 {
		t.Fatalf("summary requests = %d, want 6", stats.Summary.Requests)
	}
	if stats.Summary.Status2xx != 4 || stats.Summary.Status4xx != 1 || stats.Summary.Status5xx != 1 || stats.Summary.StatusOther != 0 {
		t.Fatalf("summary = %+v", stats.Summary)
	}

	byStart := map[int]RequestStatsBucket{}
	for _, b := range stats.Buckets {
		byStart[b.Start.UTC().Hour()] = b
	}
	if b := byStart[10]; b.Requests != 3 || b.Status2xx != 2 || b.Status4xx != 1 {
		t.Fatalf("10:00 bucket = %+v", b)
	}
	if b := byStart[11]; b.Requests != 3 || b.Status2xx != 2 || b.Status5xx != 1 {
		t.Fatalf("11:00 bucket = %+v", b)
	}

	if len(stats.ProviderLatency) != 2 {
		t.Fatalf("provider series = %d, want 2", len(stats.ProviderLatency))
	}
	if stats.ProviderLatency[0].Provider != "openai-prod" {
		t.Fatalf("first provider = %q, want openai-prod", stats.ProviderLatency[0].Provider)
	}
	openai := stats.ProviderLatency[0]
	if openai.AvgDurationMs[10] == nil || *openai.AvgDurationMs[10] != 200 {
		t.Fatalf("openai 10:00 avg = %v, want 200 (2xx only)", openai.AvgDurationMs[10])
	}
	// The 11:00 openai bucket only saw a 502 and a cache hit -> gap.
	if openai.AvgDurationMs[11] != nil {
		t.Fatalf("openai 11:00 avg = %v, want nil", openai.AvgDurationMs[11])
	}
	anthropic := stats.ProviderLatency[1]
	if anthropic.Provider != "anthropic" {
		t.Fatalf("second provider = %q, want anthropic", anthropic.Provider)
	}
	if anthropic.AvgDurationMs[11] == nil || *anthropic.AvgDurationMs[11] != 700 {
		t.Fatalf("anthropic 11:00 avg = %v, want 700", anthropic.AvgDurationMs[11])
	}
}
