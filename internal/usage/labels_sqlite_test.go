package usage

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteUsageLabelsRoundTrip(t *testing.T) {
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
			ID:           "labelled",
			RequestID:    "req-labelled",
			ProviderID:   "provider-1",
			Timestamp:    time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			Labels:       []string{"alpha", "prod"},
			TotalTokens:  10,
			OutputTokens: 10,
		},
		{
			ID:           "unlabelled",
			RequestID:    "req-unlabelled",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 1, 16, 12, 1, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			TotalTokens:  20,
			OutputTokens: 20,
		},
	})
	if err != nil {
		t.Fatalf("failed to write usage entries: %v", err)
	}

	reader := &SQLiteReader{db: db}
	result, err := reader.GetUsageLog(ctx, UsageLogParams{})
	if err != nil {
		t.Fatalf("GetUsageLog() error = %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(result.Entries))
	}

	byID := make(map[string]UsageLogEntry, len(result.Entries))
	for _, entry := range result.Entries {
		byID[entry.ID] = entry
	}
	if got, want := byID["labelled"].Labels, []string{"alpha", "prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labelled entry labels = %#v, want %#v", got, want)
	}
	if got := byID["unlabelled"].Labels; got != nil {
		t.Fatalf("unlabelled entry labels = %#v, want nil", got)
	}
}
