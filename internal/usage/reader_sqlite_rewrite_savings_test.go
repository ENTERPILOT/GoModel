package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Covers the rewrite-savings columns on both usage log read paths: the
// paginated GetUsageLog SELECT and the GetUsageByRequestIDs lookup that backs
// the audit API's per-request usage summary must surface rewrite_tokens_saved
// and the nullable rewrite_cost_saved.
func TestSQLiteReader_UsageLogCarriesRewriteSavings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	cost := 0.0375
	ctx := context.Background()
	err = store.WriteBatch(ctx, []*UsageEntry{
		{
			ID:                 "with-savings",
			RequestID:          "req-saved",
			ProviderID:         "provider-1",
			Timestamp:          time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:              "gpt-5",
			Provider:           "openai",
			Endpoint:           "/v1/chat/completions",
			InputTokens:        100,
			OutputTokens:       10,
			TotalTokens:        110,
			RewriteTokensSaved: 89,
			RewriteCostSaved:   &cost,
		},
		{
			ID:           "without-savings",
			RequestID:    "req-plain",
			ProviderID:   "provider-2",
			Timestamp:    time.Date(2026, 1, 16, 12, 1, 0, 0, time.UTC),
			Model:        "gpt-5",
			Provider:     "openai",
			Endpoint:     "/v1/chat/completions",
			InputTokens:  50,
			OutputTokens: 10,
			TotalTokens:  60,
		},
	})
	if err != nil {
		t.Fatalf("failed to write usage entries: %v", err)
	}

	reader := &SQLiteReader{db: db}

	log, err := reader.GetUsageLog(ctx, UsageLogParams{})
	if err != nil {
		t.Fatalf("GetUsageLog() error = %v", err)
	}
	logByRequest := make(map[string]UsageLogEntry, len(log.Entries))
	for _, entry := range log.Entries {
		logByRequest[entry.RequestID] = entry
	}

	grouped, err := reader.GetUsageByRequestIDs(ctx, []string{"req-saved", "req-plain"})
	if err != nil {
		t.Fatalf("GetUsageByRequestIDs() error = %v", err)
	}

	cases := []struct {
		name       string
		requestID  string
		wantTokens int64
		wantCost   *float64
	}{
		{name: "priced savings round-trip", requestID: "req-saved", wantTokens: 89, wantCost: &cost},
		{name: "no savings stays zero with nil cost", requestID: "req-plain", wantTokens: 0, wantCost: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			byRequest, ok := grouped[tc.requestID]
			if !ok || len(byRequest) != 1 {
				t.Fatalf("GetUsageByRequestIDs()[%q] = %v, want one entry", tc.requestID, byRequest)
			}
			logEntry, ok := logByRequest[tc.requestID]
			if !ok {
				t.Fatalf("GetUsageLog() missing request %q", tc.requestID)
			}
			for path, entry := range map[string]UsageLogEntry{"usage log": logEntry, "by request id": byRequest[0]} {
				if entry.RewriteTokensSaved != tc.wantTokens {
					t.Errorf("%s RewriteTokensSaved = %d, want %d", path, entry.RewriteTokensSaved, tc.wantTokens)
				}
				switch {
				case tc.wantCost == nil:
					if entry.RewriteCostSaved != nil {
						t.Errorf("%s RewriteCostSaved = %v, want nil", path, *entry.RewriteCostSaved)
					}
				case entry.RewriteCostSaved == nil:
					t.Errorf("%s RewriteCostSaved = nil, want %v", path, *tc.wantCost)
				case *entry.RewriteCostSaved != *tc.wantCost:
					t.Errorf("%s RewriteCostSaved = %v, want %v", path, *entry.RewriteCostSaved, *tc.wantCost)
				}
			}
		})
	}
}
