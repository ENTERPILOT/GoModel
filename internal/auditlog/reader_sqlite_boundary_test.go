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
			ID:             "start-boundary",
			Timestamp:      time.Date(2026, 1, 15, 23, 0, 0, 123_000_000, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
		},
		{
			ID:             "inside-range",
			Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
		},
		{
			ID:             "after-end-boundary",
			Timestamp:      time.Date(2026, 1, 16, 23, 0, 0, 123_000_000, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
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

func TestSQLiteReaderGetLogs_SearchMatchesUserPath(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*LogEntry{
		{
			ID:             "team-match",
			Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			UserPath:       "/team/alpha",
		},
		{
			ID:             "other-team",
			Timestamp:      time.Date(2026, 1, 16, 11, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			UserPath:       "/org/beta",
		},
	}); err != nil {
		t.Fatalf("failed to seed audit logs: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	result, err := reader.GetLogs(ctx, LogQueryParams{
		Search: "team/alpha",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("GetLogs returned error: %v", err)
	}

	if result.Total != 1 {
		t.Fatalf("expected 1 log in search result, got %d", result.Total)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 returned entry, got %d", len(result.Entries))
	}
	if result.Entries[0].ID != "team-match" {
		t.Fatalf("expected matching entry %q, got %q", "team-match", result.Entries[0].ID)
	}
}

func TestSQLiteReaderGetLogs_SearchMatchesErrorMessage(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	if err := store.WriteBatch(ctx, []*LogEntry{
		{
			ID:             "timeout-match",
			Timestamp:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			ErrorType:      "provider_error",
			Data: &LogData{
				ErrorMessage: `failed to send request: Post "https://api.openai.com/v1/chat/completions": http2: timeout awaiting response headers`,
			},
		},
		{
			ID:             "other-error",
			Timestamp:      time.Date(2026, 1, 16, 11, 0, 0, 0, time.UTC),
			RequestedModel: "gpt-5",
			Provider:       "openai",
			ErrorType:      "provider_error",
			Data: &LogData{
				ErrorMessage: "upstream refused connection",
			},
		},
	}); err != nil {
		t.Fatalf("failed to seed audit logs: %v", err)
	}

	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatalf("failed to create sqlite reader: %v", err)
	}

	result, err := reader.GetLogs(ctx, LogQueryParams{
		Search: "timeout awaiting response headers",
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("GetLogs returned error: %v", err)
	}

	if result.Total != 1 {
		t.Fatalf("expected 1 log in search result, got %d", result.Total)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("expected 1 returned entry, got %d", len(result.Entries))
	}
	if result.Entries[0].ID != "timeout-match" {
		t.Fatalf("expected matching entry %q, got %q", "timeout-match", result.Entries[0].ID)
	}
}
