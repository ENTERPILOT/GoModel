package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteReaderGetDailyUsage_GroupsByConfiguredTimeZone(t *testing.T) {
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
			ID:           "entry-1",
			RequestID:    "req-1",
			ProviderID:   "provider-1",
			Timestamp:    time.Date(2026, 1, 15, 22, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
		{
			ID:           "entry-2",
			RequestID:    "req-2",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  20,
			OutputTokens: 10,
			TotalTokens:  30,
		},
		{
			ID:           "entry-3",
			RequestID:    "req-3",
			ProviderID:   "provider-3",
			Timestamp:    time.Date(2026, 1, 16, 10, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  25,
			OutputTokens: 15,
			TotalTokens:  40,
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
		StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		Interval:  "daily",
		TimeZone:  "Europe/Warsaw",
	})
	if err != nil {
		t.Fatalf("GetDailyUsage returned error: %v", err)
	}

	if len(daily) != 1 {
		t.Fatalf("expected 1 grouped period, got %d", len(daily))
	}

	if daily[0].Date != "2026-01-16" {
		t.Errorf("expected grouped date %q, got %q", "2026-01-16", daily[0].Date)
	}
	if daily[0].Requests != 2 {
		t.Errorf("expected 2 requests in grouped period, got %d", daily[0].Requests)
	}
	if daily[0].TotalTokens != 70 {
		t.Errorf("expected 70 total tokens in grouped period, got %d", daily[0].TotalTokens)
	}
}
