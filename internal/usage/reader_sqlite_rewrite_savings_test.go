package usage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// Covers the rewrite-savings columns on both usage log read paths: the
// paginated GetUsageLog SELECT and the GetUsageByRequestIDs lookup that backs
// the audit API's per-request usage summary must surface rewrite_tokens_saved
// and the nullable rewrite_cost_saved.
func TestSQLiteReader_UsageLogCarriesRewriteSavings(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	require.NoError(t, err)

	reader := &SQLiteReader{db: db}

	log, err := reader.GetUsageLog(ctx, UsageLogParams{})
	require.NoError(t, err)

	logByRequest := make(map[string]UsageLogEntry, len(log.Entries))
	for _, entry := range log.Entries {
		logByRequest[entry.RequestID] = entry
	}

	grouped, err := reader.GetUsageByRequestIDs(ctx, []string{"req-saved", "req-plain"})
	require.NoError(t, err)

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
			require.True(t, ok)
			require.Len(t, byRequest, 1, "request %q", tc.requestID)

			logEntry, ok := logByRequest[tc.requestID]
			require.True(t, ok, "GetUsageLog() missing request %q", tc.requestID)

			for path, entry := range map[string]UsageLogEntry{"usage log": logEntry, "by request id": byRequest[0]} {
				assert.Equal(t, tc.wantTokens, entry.RewriteTokensSaved, path)

				if tc.wantCost == nil {
					assert.Nil(t, entry.RewriteCostSaved, path)
				} else {
					require.NotNil(t, entry.RewriteCostSaved, path)
					assert.Equal(t, *tc.wantCost, *entry.RewriteCostSaved, path)
				}
			}
		})
	}
}
