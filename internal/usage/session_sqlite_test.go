package usage

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteSessionUsageRoundTripAggregationAndFilter(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}

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
	if err := store.WriteBatch(context.Background(), entries); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	result, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{
		UsageQueryParams: UsageQueryParams{CacheMode: CacheModeUncached},
	})
	if err != nil {
		t.Fatalf("GetUsageBySession: %v", err)
	}
	if result.Total != 3 || result.Limit != 50 || result.Offset != 0 || len(result.Entries) != 3 {
		t.Fatalf("session result = %+v", result)
	}
	byID := make(map[string]SessionUsage, len(result.Entries))
	for _, row := range result.Entries {
		byID[row.SessionID] = row
	}
	sessionA := byID["scoped-session-a"]
	if sessionA.UserPath != "/team/a" || sessionA.Requests != 3 || sessionA.InputTokens != 37 || sessionA.OutputTokens != 13 || sessionA.TotalTokens != 50 {
		t.Fatalf("session A = %+v", sessionA)
	}
	if sessionA.TotalCost == nil || math.Abs(*sessionA.TotalCost-0.40) > 1e-12 {
		t.Fatalf("session A total cost = %v, want provider spend 0.40", sessionA.TotalCost)
	}
	sessionC := byID["scoped-session-c"]
	if sessionC.Requests != 1 || sessionC.TotalCost == nil || *sessionC.TotalCost != 0 {
		t.Fatalf("cache-only session C = %+v, want one request and zero provider spend", sessionC)
	}

	page, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("GetUsageBySession page: %v", err)
	}
	if page.Total != 3 || page.Limit != 1 || page.Offset != 1 || len(page.Entries) != 1 {
		t.Fatalf("session page = %+v", page)
	}

	logResult, err := reader.GetUsageLog(context.Background(), UsageLogParams{UsageQueryParams: UsageQueryParams{
		SessionID: "scoped-session-b",
		CacheMode: CacheModeAll,
	}})
	if err != nil {
		t.Fatalf("GetUsageLog session filter: %v", err)
	}
	if logResult.Total != 1 || len(logResult.Entries) != 1 || logResult.Entries[0].SessionID != "scoped-session-b" {
		t.Fatalf("filtered log = %+v", logResult)
	}
}

func TestSQLiteSessionUsagePaginationHasStableTimestampTies(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}

	timestamp := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	entries := []*UsageEntry{
		{ID: "d", RequestID: "r-d", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-c", UserPath: "/a"},
		{ID: "c", RequestID: "r-c", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-b", UserPath: "/a"},
		{ID: "b", RequestID: "r-b", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-a", UserPath: "/b"},
		{ID: "a", RequestID: "r-a", Timestamp: timestamp, Model: "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions", SessionID: "session-a", UserPath: "/a"},
	}
	if err := store.WriteBatch(context.Background(), entries); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	want := []string{"session-a:/a", "session-a:/b", "session-b:/a", "session-c:/a"}
	var got []string
	for offset := 0; offset < len(want); offset += 2 {
		page, err := reader.GetUsageBySession(context.Background(), SessionUsageParams{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("GetUsageBySession offset %d: %v", offset, err)
		}
		for _, row := range page.Entries {
			got = append(got, row.SessionID+":"+row.UserPath)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("paged rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paged rows = %v, want %v", got, want)
		}
	}
}
