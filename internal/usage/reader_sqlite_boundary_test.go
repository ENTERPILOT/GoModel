package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteReaderSummary_IncludesFractionalStartBoundaryAndExcludesFractionalEndBoundary(t *testing.T) {
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
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "start-boundary",
			RequestID:    "req-start",
			ProviderID:   "provider-start",
			Timestamp:    time.Date(2026, 1, 15, 23, 0, 0, 123_000_000, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  10,
			OutputTokens: 10,
		},
		{
			ID:           "inside-range",
			RequestID:    "req-inside",
			ProviderID:   "provider-inside",
			Timestamp:    time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  20,
			OutputTokens: 20,
		},
		{
			ID:           "after-end-boundary",
			RequestID:    "req-after",
			ProviderID:   "provider-after",
			Timestamp:    time.Date(2026, 1, 16, 23, 0, 0, 123_000_000, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  999,
			OutputTokens: 999,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

	summary, err := reader.GetSummary(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		TimeZone:  "Europe/Warsaw",
	})
	if err != nil {
		t.Fatalf("GetSummary returned error: %v", err)
	}

	if summary.TotalRequests != 2 {
		t.Fatalf("expected 2 requests in range, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 30 {
		t.Fatalf("expected 30 total tokens in range, got %d", summary.TotalTokens)
	}
}

func TestSQLiteReaderGetDailyUsage_GroupsAcrossDSTTransitionInConfiguredTimeZone(t *testing.T) {
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
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:           "before-dst-switch",
			RequestID:    "req-before",
			ProviderID:   "provider-before",
			Timestamp:    time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  10,
			OutputTokens: 10,
		},
		{
			ID:           "after-dst-switch",
			RequestID:    "req-after",
			ProviderID:   "provider-after",
			Timestamp:    time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  20,
			OutputTokens: 20,
		},
	})
	if err != nil {
		t.Fatalf("failed to seed usage entries: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

	daily, err := reader.GetDailyUsage(ctx, UsageQueryParams{
		StartDate: time.Date(2026, 3, 29, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, location),
		Interval:  "daily",
		TimeZone:  "Europe/Warsaw",
	})
	if err != nil {
		t.Fatalf("GetDailyUsage returned error: %v", err)
	}

	if len(daily) != 1 {
		t.Fatalf("expected 1 grouped period, got %d", len(daily))
	}
	if daily[0].Date != "2026-03-29" {
		t.Fatalf("expected grouped date %q, got %q", "2026-03-29", daily[0].Date)
	}
	if daily[0].Requests != 2 {
		t.Fatalf("expected 2 requests in grouped period, got %d", daily[0].Requests)
	}
	if daily[0].TotalTokens != 30 {
		t.Fatalf("expected 30 total tokens in grouped period, got %d", daily[0].TotalTokens)
	}
}
