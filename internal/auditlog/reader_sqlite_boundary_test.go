package auditlog

import (
	"context"
	"testing"
	"time"
)

func TestSQLiteReaderGetLogs_IncludesFractionalStartBoundaryAndExcludesFractionalEndBoundary(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	err = store.WriteBatch(ctx, []*LogEntry{
		{
			ID:        "start-boundary",
			Timestamp: time.Date(2026, 1, 15, 23, 0, 0, 123_000_000, time.UTC),
			Model:     "gpt-5",
			Provider:  "openai",
		},
		{
			ID:        "inside-range",
			Timestamp: time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:     "gpt-5",
			Provider:  "openai",
		},
		{
			ID:        "after-end-boundary",
			Timestamp: time.Date(2026, 1, 16, 23, 0, 0, 123_000_000, time.UTC),
			Model:     "gpt-5",
			Provider:  "openai",
		},
	})
	if err != nil {
		t.Fatalf("failed to seed audit logs: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	location, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatalf("failed to load location: %v", err)
	}

	result, err := reader.GetLogs(ctx, LogQueryParams{
		QueryParams: QueryParams{
			StartDate: time.Date(2026, 1, 16, 0, 0, 0, 0, location),
			EndDate:   time.Date(2026, 1, 16, 0, 0, 0, 0, location),
		},
		Limit:  10,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("GetLogs returned error: %v", err)
	}

	if result.Total != 2 {
		t.Fatalf("expected 2 logs in range, got %d", result.Total)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 returned entries, got %d", len(result.Entries))
	}
	if result.Entries[0].ID != "inside-range" {
		t.Fatalf("expected latest in-range entry %q, got %q", "inside-range", result.Entries[0].ID)
	}
	if result.Entries[1].ID != "start-boundary" {
		t.Fatalf("expected boundary entry %q, got %q", "start-boundary", result.Entries[1].ID)
	}
}
