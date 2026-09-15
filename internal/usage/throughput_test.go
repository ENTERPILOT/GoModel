package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseThroughputGranularity(t *testing.T) {
	cases := map[string]struct {
		wantWindow int
		wantBucket time.Duration
		wantErr    bool
	}{
		"second":  {wantWindow: 60, wantBucket: time.Second},
		"minute":  {wantWindow: 60, wantBucket: time.Minute},
		"hour":    {wantWindow: 24, wantBucket: time.Hour},
		"day":     {wantWindow: 30, wantBucket: 24 * time.Hour},
		"MINUTE ": {wantWindow: 60, wantBucket: time.Minute},
		"weekly":  {wantErr: true},
		"":        {wantErr: true},
	}
	for name, tc := range cases {
		gran, err := ParseThroughputGranularity(name)
		if tc.wantErr {
			assert.Error(t, err, "granularity %q", name)
			continue
		}
		if !assert.NoError(t, err, "granularity %q", name) {
			continue
		}
		assert.Equal(t, tc.wantWindow, gran.WindowCount, "granularity %q", name)
		assert.Equal(t, tc.wantBucket, gran.BucketSize, "granularity %q", name)
	}
}

func TestEmptyTokenThroughputIsZeroFilledAndAligned(t *testing.T) {
	gran, _ := ParseThroughputGranularity("minute")
	end := time.Date(2026, 1, 15, 12, 0, 30, 0, time.UTC)
	tp := EmptyTokenThroughput(gran, end, 0)

	require.Equal(t, 60, tp.BucketSeconds)
	require.Equal(t, "minute", tp.Granularity)
	require.Len(t, tp.Buckets, 60)

	// Last bucket starts at the current minute; first is 59 minutes earlier.
	wantLast := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	assert.True(t, tp.Buckets[59].Start.Equal(wantLast), "last bucket start = %v, want %v", tp.Buckets[59].Start, wantLast)
	assert.True(t, tp.Buckets[0].Start.Equal(wantLast.Add(-59*time.Minute)), "first bucket start = %v, want %v", tp.Buckets[0].Start, wantLast.Add(-59*time.Minute))

	for i, b := range tp.Buckets {
		assert.Equal(t, int64(0), b.InputTokens)
		assert.Equal(t, int64(0), b.OutputTokens)
		assert.Equal(t, int64(0), b.PromptCachedTokens)
		assert.Equal(t, int64(0), b.LocallyCachedTokens, "bucket %d not zero-filled: %+v", i, b)
	}
}

func TestSQLiteReaderGetTokenThroughput_SplitsAndBuckets(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	end := time.Date(2026, 1, 15, 12, 0, 30, 0, time.UTC)
	inWindow := time.Date(2026, 1, 15, 12, 0, 10, 0, time.UTC)  // current minute bucket
	outOfWindow := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC) // hours earlier, excluded

	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID: "provider-row", RequestID: "r1", Timestamp: inWindow,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 100, OutputTokens: 40, TotalTokens: 140,
			RawData: map[string]any{"cached_tokens": 30}, // 70 uncached + 30 prompt-cached
		},
		{
			ID: "local-cache-row", RequestID: "r2", Timestamp: inWindow,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			CacheType: CacheTypeExact, InputTokens: 12, OutputTokens: 8, TotalTokens: 20,
		},
		{
			ID: "old-row", RequestID: "r3", Timestamp: outOfWindow,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 999, OutputTokens: 999, TotalTokens: 1998,
		},
	})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	gran, _ := ParseThroughputGranularity("minute")
	tp, err := reader.GetTokenThroughput(ctx, gran, end, 0)
	require.NoError(t, err)
	require.Len(t, tp.Buckets, 60)

	last := tp.Buckets[59]
	assert.Equal(t, int64(70), last.InputTokens)
	assert.Equal(t, int64(30), last.PromptCachedTokens)
	assert.Equal(t, int64(40), last.OutputTokens)
	assert.Equal(t, int64(20), last.LocallyCachedTokens)

	// Every earlier bucket must be empty — the out-of-window row is excluded.
	for i := range 59 {
		b := tp.Buckets[i]
		assert.Equal(t, int64(0), b.InputTokens+b.OutputTokens+b.PromptCachedTokens+b.LocallyCachedTokens, "bucket %d should be empty, got %+v", i, b)
	}
}

// Day buckets must start at local midnight (per the timezone offset), so a
// request late on the local day lands in "today" even if it's already the next
// UTC day — matching the Daily Token Usage chart.
func TestSQLiteReaderGetTokenThroughput_DayBucketsUseTimezoneOffset(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	ctx := context.Background()
	offset := int64(2 * 3600)                            // UTC+2
	end := time.Date(2026, 1, 15, 0, 30, 0, 0, time.UTC) // 02:30 local on Jan 15
	// 23:00 UTC Jan 14 == 01:00 local Jan 15, i.e. local "today".
	rowTime := time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC)
	err = store.WriteBatch(ctx, []*UsageEntry{{
		ID: "tz1", RequestID: "r1", Timestamp: rowTime, Model: "gpt-5", Provider: "openai",
		Endpoint: "/v1/chat/completions", InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}})
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	gran, _ := ParseThroughputGranularity("day")

	tp, err := reader.GetTokenThroughput(ctx, gran, end, offset)
	require.NoError(t, err)

	today := tp.Buckets[len(tp.Buckets)-1]
	wantStart := time.Date(2026, 1, 14, 22, 0, 0, 0, time.UTC) // local midnight Jan 15
	assert.True(t, today.Start.Equal(wantStart), "today bucket start = %v, want local midnight %v", today.Start, wantStart)
	assert.Equal(t, int64(10), today.InputTokens)

	// With UTC alignment (offset 0) the same row falls in *yesterday*, so today is empty.
	utc, err := reader.GetTokenThroughput(ctx, gran, end, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), utc.Buckets[len(utc.Buckets)-1].InputTokens)
}
