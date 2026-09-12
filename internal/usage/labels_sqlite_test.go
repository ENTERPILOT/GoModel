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

func TestSQLiteUsageLabelsRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	defer db.Close()

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

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
	require.NoError(t, err)

	reader := &SQLiteReader{db: db}
	result, err := reader.GetUsageLog(ctx, UsageLogParams{})
	require.NoError(t, err)
	require.Len(t, result.Entries, 2)

	byID := make(map[string]UsageLogEntry, len(result.Entries))
	for _, entry := range result.Entries {
		byID[entry.ID] = entry
	}
	require.Equal(t, []string{"alpha", "prod"}, byID["labelled"].Labels)
	require.Nil(t, byID["unlabelled"].Labels)
}

// newLabelledSQLiteReader seeds an in-memory usage table with a mix of
// labelled and unlabelled entries shared by the by-label and label-filter tests.
func newLabelledSQLiteReader(t *testing.T) *SQLiteReader {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	t.Cleanup(func() { db.Close() })

	store, err := NewSQLiteStore(db, 0)
	require.NoError(t, err)

	cost := func(v float64) *float64 { return &v }
	err = store.WriteBatch(context.Background(), []*UsageEntry{
		{
			ID: "e1", RequestID: "req-1", ProviderID: "p1",
			Timestamp: time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC),
			Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			Labels:      []string{"alpha", "prod"},
			InputTokens: 100, OutputTokens: 10, TotalTokens: 110,
			InputCost: cost(0.1), OutputCost: cost(0.2), TotalCost: cost(0.3),
		},
		{
			ID: "e2", RequestID: "req-2", ProviderID: "p2",
			Timestamp: time.Date(2026, 1, 16, 13, 0, 0, 0, time.UTC),
			Model:     "claude-haiku", Provider: "anthropic", Endpoint: "/v1/chat/completions",
			Labels:      []string{"alpha"},
			InputTokens: 50, OutputTokens: 5, TotalTokens: 55,
		},
		{
			ID: "e3", RequestID: "req-3", ProviderID: "p3",
			Timestamp: time.Date(2026, 1, 16, 14, 0, 0, 0, time.UTC),
			Model:     "gpt-5", Provider: "openai", Endpoint: "/v1/chat/completions",
			InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100,
		},
	})
	require.NoError(t, err)

	return &SQLiteReader{db: db}
}

func TestSQLiteGetUsageByLabel(t *testing.T) {
	reader := newLabelledSQLiteReader(t)

	result, err := reader.GetUsageByLabel(context.Background(), UsageQueryParams{})
	require.NoError(t, err)
	require.Len(t, result, 2)

	alpha := result[0]
	require.Equal(t, "alpha", alpha.Label)
	assert.Equal(t, 2, alpha.Requests)
	assert.Equal(t, int64(150), alpha.InputTokens)
	assert.Equal(t, int64(15), alpha.OutputTokens)
	assert.Equal(t, int64(165), alpha.TotalTokens, "alpha aggregates = %+v, want requests 2, input 150, output 15, total 165", alpha)
	assert.NotNil(t, alpha.TotalCost)
	assert.Equal(t, 0.3, *alpha.TotalCost)

	prod := result[1]
	require.Equal(t, "prod", prod.Label)
	assert.Equal(t, 1, prod.Requests)
	assert.Equal(t, int64(110), prod.TotalTokens, "prod aggregates = %+v, want requests 1, total 110", prod)
}

// TestSQLiteAggregatesRespectDataFilters exercises the shared filter path:
// model/provider/label filters must shape every aggregate the same way they
// shape the request log.
func TestSQLiteAggregatesRespectDataFilters(t *testing.T) {
	reader := newLabelledSQLiteReader(t)
	ctx := context.Background()

	// Label filter narrows the summary to the single "prod" entry.
	summary, err := reader.GetSummary(ctx, UsageQueryParams{Label: "prod"})
	require.NoError(t, err)
	assert.Equal(t, 1, summary.TotalRequests)
	assert.Equal(t, int64(110), summary.TotalTokens, "summary with label filter = %+v, want 1 request / 110 tokens", summary)

	// Label filter narrows the by-model breakdown to entries carrying it.
	models, err := reader.GetUsageByModel(ctx, UsageQueryParams{Label: "alpha"})
	require.NoError(t, err)
	require.Len(t, models, 2)

	// Model filter narrows the by-model breakdown to that model's rows.
	models, err = reader.GetUsageByModel(ctx, UsageQueryParams{Model: "gpt-5"})
	require.NoError(t, err)
	assert.Len(t, models, 1)
	assert.Equal(t, "gpt-5", models[0].Model)
	assert.Equal(t, int64(1100), models[0].InputTokens)

	// Provider filter narrows the by-label breakdown to that provider's labels.
	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{Provider: "anthropic"})
	require.NoError(t, err)
	assert.Len(t, labels, 1)
	assert.Equal(t, "alpha", labels[0].Label)
	assert.Equal(t, 1, labels[0].Requests)
}

func TestSQLiteUsageLogLabelFilter(t *testing.T) {
	reader := newLabelledSQLiteReader(t)

	result, err := reader.GetUsageLog(context.Background(), UsageLogParams{Label: "prod"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "e1", result.Entries[0].ID)

	// A label that is a substring of an existing one must not match.
	empty, err := reader.GetUsageLog(context.Background(), UsageLogParams{Label: "pro"})
	require.NoError(t, err)
	require.Equal(t, 0, empty.Total)
	require.Empty(t, empty.Entries)
}
