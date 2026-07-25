package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestGetCacheOverviewIgnoresRequestedCacheMode pins the contract documented on
// the UsageReader interface: cached-only scope belongs to GetCacheOverview, not
// to its caller. The admin handler used to set CacheMode itself, which made the
// guarantee look like the caller's job and left it asserted only through a stub
// reader that could not enforce it.
func TestGetCacheOverviewIgnoresRequestedCacheMode(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	day := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*UsageEntry{
		{
			ID: "cached-hit", RequestID: "r1", ProviderID: "p1", Timestamp: day,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			CacheType: CacheTypeExact, InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
		},
		{
			ID: "uncached-miss", RequestID: "r2", ProviderID: "p1", Timestamp: day,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 200, OutputTokens: 40, TotalTokens: 240,
		},
	}); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}

	// Every mode a caller could pass, including the one that would otherwise
	// widen the result to uncached rows.
	for _, mode := range []string{"", CacheModeAll, CacheModeUncached, CacheModeCached} {
		t.Run("mode="+mode, func(t *testing.T) {
			overview, err := reader.GetCacheOverview(ctx, UsageQueryParams{
				StartDate: day,
				EndDate:   day,
				CacheMode: mode,
			})
			if err != nil {
				t.Fatalf("GetCacheOverview: %v", err)
			}
			if overview.Summary.TotalHits != 1 {
				t.Fatalf("total_hits = %d, want 1 (the cached row only)", overview.Summary.TotalHits)
			}
		})
	}
}
