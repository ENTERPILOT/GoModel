package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteSessionUsageRoundTripAggregationAndFilter(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	cost := func(value float64) *float64 { return &value }
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	entries := []*UsageEntry{
		{
			ID: "u1", RequestID: "r1", ProviderID: "p1", Timestamp: now,
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/a", SessionID: "scoped-session-a",
			InputTokens: 10, OutputTokens: 4, TotalTokens: 14,
			InputCost: cost(0.10), OutputCost: cost(0.04), TotalCost: cost(0.14),
		},
		{
			ID: "u2", RequestID: "r2", ProviderID: "p2", Timestamp: now.Add(time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/a", SessionID: "scoped-session-a",
			InputTokens: 20, OutputTokens: 6, TotalTokens: 26,
			InputCost: cost(0.20), OutputCost: cost(0.06), TotalCost: cost(0.26),
		},
		{
			ID: "u-cache", RequestID: "r-cache", ProviderID: "p-cache", Timestamp: now.Add(90 * time.Second),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/a", SessionID: "scoped-session-a", CacheType: CacheTypeExact,
			InputTokens: 7, OutputTokens: 3, TotalTokens: 10,
			InputCost: cost(0.07), OutputCost: cost(0.03), TotalCost: cost(0.10),
		},
		{
			ID: "u3", RequestID: "r3", ProviderID: "p3", Timestamp: now.Add(2 * time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/b", SessionID: "scoped-session-b",
			InputTokens: 5, OutputTokens: 1, TotalTokens: 6,
		},
		{
			ID: "u-cache-only", RequestID: "r-cache-only", ProviderID: "p-cache-only", Timestamp: now.Add(150 * time.Second),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/c", SessionID: "scoped-session-c", CacheType: CacheTypeSemantic,
			InputTokens: 9, OutputTokens: 2, TotalTokens: 11,
			InputCost: cost(0.09), OutputCost: cost(0.02), TotalCost: cost(0.11),
		},
		{
			ID: "legacy", RequestID: "r4", ProviderID: "p4", Timestamp: now.Add(3 * time.Minute),
			Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			UserPath: "/team/a", TotalTokens: 99,
		},
	}
	err = store.WriteBatch(context.Background(), entries)
	require.NoError(t, err)

	result, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{
		CacheMode: CacheModeUncached,
	})
	require.NoError(t, err)
	require.Equal(t, 3, result.Total)
	require.Equal(t, 50, result.Limit)
	require.Equal(t, 0, result.Offset)
	require.Len(t, result.Entries, 3, "session result = %+v", result)

	byID := make(map[string]SessionUsage, len(result.Entries))
	for _, row := range result.Entries {
		byID[row.SessionID] = row
	}
	sessionA := byID["scoped-session-a"]
	require.Equal(t, "/team/a", sessionA.UserPath)
	require.Equal(t, 3, sessionA.Requests)
	require.Equal(t, int64(37), sessionA.InputTokens)
	require.Equal(t, int64(13), sessionA.OutputTokens)
	require.Equal(t, int64(50), sessionA.TotalTokens, "session A = %+v", sessionA)
	require.NotNil(t, sessionA.TotalCost)
	require.InDelta(t, 0.40, *sessionA.TotalCost, 1e-12)

	sessionC := byID["scoped-session-c"]
	require.Equal(t, 1, sessionC.Requests)
	require.NotNil(t, sessionC.TotalCost)
	require.Equal(t, float64(0), *sessionC.TotalCost, "cache-only session C = %+v, want one request and zero provider spend", sessionC)

	page, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	require.Equal(t, 1, page.Limit)
	require.Equal(t, 1, page.Offset)
	require.Len(t, page.Entries, 1, "session page = %+v", page)

	logResult, err := reader.GetUsageLog(context.Background(), UsageLogParams{
		SessionID: "scoped-session-b",
		CacheMode: CacheModeAll})
	require.NoError(t, err)
	require.Equal(t, 1, logResult.Total)
	require.Len(t, logResult.Entries, 1)
	require.Equal(t, "scoped-session-b", logResult.Entries[0].SessionID, "filtered log = %+v", logResult)
}

func TestSQLiteSessionUsagePaginationHasStableTimestampTies(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	reader, err := NewSQLiteReader(db)
	require.NoError(t, err)

	timestamp := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	entries := []*UsageEntry{
		{ID: "d", RequestID: "r-d", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-c", UserPath: "/a"},
		{ID: "c", RequestID: "r-c", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-b", UserPath: "/a"},
		{ID: "b", RequestID: "r-b", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-a", UserPath: "/b"},
		{ID: "a", RequestID: "r-a", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-a", UserPath: "/a"},
	}
	err = store.WriteBatch(context.Background(), entries)
	require.NoError(t, err)

	want := []string{"session-a:/a", "session-a:/b", "session-b:/a", "session-c:/a"}
	var got []string
	for offset := 0; offset < len(want); offset += 2 {
		page, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{Limit: 2, Offset: offset})
		require.NoError(t, err, "offset %d", offset)

		for _, row := range page.Entries {
			got = append(got, row.SessionID+":"+row.UserPath)
		}
	}
	require.Equal(t, want, got, "paged rows")
}
