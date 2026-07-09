package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"
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
			if err == nil {
				t.Errorf("ParseThroughputGranularity(%q) expected error, got none", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseThroughputGranularity(%q) unexpected error: %v", name, err)
			continue
		}
		if gran.WindowCount != tc.wantWindow || gran.BucketSize != tc.wantBucket {
			t.Errorf("ParseThroughputGranularity(%q) = {window:%d bucket:%v}, want {window:%d bucket:%v}",
				name, gran.WindowCount, gran.BucketSize, tc.wantWindow, tc.wantBucket)
		}
	}
}

func TestEmptyTokenThroughputIsZeroFilledAndAligned(t *testing.T) {
	gran, _ := ParseThroughputGranularity("minute")
	end := time.Date(2026, 1, 15, 12, 0, 30, 0, time.UTC)
	tp := EmptyTokenThroughput(gran, end, 0)

	if tp.BucketSeconds != 60 || tp.Granularity != "minute" {
		t.Fatalf("got granularity=%q bucketSeconds=%d", tp.Granularity, tp.BucketSeconds)
	}
	if len(tp.Buckets) != 60 {
		t.Fatalf("got %d buckets, want 60", len(tp.Buckets))
	}
	// Last bucket starts at the current minute; first is 59 minutes earlier.
	wantLast := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if !tp.Buckets[59].Start.Equal(wantLast) {
		t.Errorf("last bucket start = %v, want %v", tp.Buckets[59].Start, wantLast)
	}
	if !tp.Buckets[0].Start.Equal(wantLast.Add(-59 * time.Minute)) {
		t.Errorf("first bucket start = %v, want %v", tp.Buckets[0].Start, wantLast.Add(-59*time.Minute))
	}
	for i, b := range tp.Buckets {
		if b.InputTokens != 0 || b.OutputTokens != 0 || b.PromptCachedTokens != 0 || b.LocallyCachedTokens != 0 {
			t.Errorf("bucket %d not zero-filled: %+v", i, b)
		}
	}
}

func TestSQLiteReaderGetTokenThroughput_SplitsAndBuckets(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

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
	if err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	gran, _ := ParseThroughputGranularity("minute")
	tp, err := reader.GetTokenThroughput(ctx, gran, end, 0)
	if err != nil {
		t.Fatalf("GetTokenThroughput returned error: %v", err)
	}
	if len(tp.Buckets) != 60 {
		t.Fatalf("got %d buckets, want 60", len(tp.Buckets))
	}

	last := tp.Buckets[59]
	if last.InputTokens != 70 {
		t.Errorf("InputTokens = %d, want 70 (uncached portion of provider input)", last.InputTokens)
	}
	if last.PromptCachedTokens != 30 {
		t.Errorf("PromptCachedTokens = %d, want 30", last.PromptCachedTokens)
	}
	if last.OutputTokens != 40 {
		t.Errorf("OutputTokens = %d, want 40", last.OutputTokens)
	}
	if last.LocallyCachedTokens != 20 {
		t.Errorf("LocallyCachedTokens = %d, want 20 (total tokens of the exact-cache hit)", last.LocallyCachedTokens)
	}

	// Every earlier bucket must be empty — the out-of-window row is excluded.
	for i := range 59 {
		b := tp.Buckets[i]
		if b.InputTokens+b.OutputTokens+b.PromptCachedTokens+b.LocallyCachedTokens != 0 {
			t.Errorf("bucket %d should be empty, got %+v", i, b)
		}
	}
}

// Day buckets must start at local midnight (per the timezone offset), so a
// request late on the local day lands in "today" even if it's already the next
// UTC day — matching the Daily Token Usage chart.
func TestSQLiteReaderGetTokenThroughput_DayBucketsUseTimezoneOffset(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	ctx := context.Background()
	offset := int64(2 * 3600)                            // UTC+2
	end := time.Date(2026, 1, 15, 0, 30, 0, 0, time.UTC) // 02:30 local on Jan 15
	// 23:00 UTC Jan 14 == 01:00 local Jan 15, i.e. local "today".
	rowTime := time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC)

	if err := store.WriteBatch(ctx, []*UsageEntry{{
		ID: "tz1", RequestID: "r1", Timestamp: rowTime, Model: "gpt-5", Provider: "openai",
		Endpoint: "/v1/chat/completions", InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}}); err != nil {
		t.Fatalf("failed to seed usage entry: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}
	gran, _ := ParseThroughputGranularity("day")

	tp, err := reader.GetTokenThroughput(ctx, gran, end, offset)
	if err != nil {
		t.Fatalf("GetTokenThroughput returned error: %v", err)
	}
	today := tp.Buckets[len(tp.Buckets)-1]
	wantStart := time.Date(2026, 1, 14, 22, 0, 0, 0, time.UTC) // local midnight Jan 15
	if !today.Start.Equal(wantStart) {
		t.Errorf("today bucket start = %v, want local midnight %v", today.Start, wantStart)
	}
	if today.InputTokens != 10 {
		t.Errorf("today (local) bucket input = %d, want 10", today.InputTokens)
	}

	// With UTC alignment (offset 0) the same row falls in *yesterday*, so today is empty.
	utc, err := reader.GetTokenThroughput(ctx, gran, end, 0)
	if err != nil {
		t.Fatalf("GetTokenThroughput (UTC) returned error: %v", err)
	}
	if utc.Buckets[len(utc.Buckets)-1].InputTokens != 0 {
		t.Errorf("UTC-aligned today bucket should be empty (row is yesterday in UTC)")
	}
}
