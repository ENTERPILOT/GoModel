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

// newLabelledSQLiteReader seeds an in-memory usage table with a mix of
// labelled and unlabelled entries shared by the by-label and label-filter tests.
func newLabelledSQLiteReader(t *testing.T) *SQLiteReader {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := NewSQLiteStore(db, 0)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

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
	if err != nil {
		t.Fatalf("failed to write usage entries: %v", err)
	}

	return &SQLiteReader{db: db}
}

func TestSQLiteGetUsageByLabel(t *testing.T) {
	reader := newLabelledSQLiteReader(t)

	result, err := reader.GetUsageByLabel(context.Background(), UsageQueryParams{})
	if err != nil {
		t.Fatalf("GetUsageByLabel() error = %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("labels len = %d, want 2 (%#v)", len(result), result)
	}

	alpha := result[0]
	if alpha.Label != "alpha" {
		t.Fatalf("first label = %q, want alpha (sorted)", alpha.Label)
	}
	if alpha.Requests != 2 || alpha.InputTokens != 150 || alpha.OutputTokens != 15 || alpha.TotalTokens != 165 {
		t.Errorf("alpha aggregates = %+v, want requests 2, input 150, output 15, total 165", alpha)
	}
	if alpha.TotalCost == nil || *alpha.TotalCost != 0.3 {
		t.Errorf("alpha total cost = %v, want 0.3", alpha.TotalCost)
	}

	prod := result[1]
	if prod.Label != "prod" {
		t.Fatalf("second label = %q, want prod", prod.Label)
	}
	if prod.Requests != 1 || prod.TotalTokens != 110 {
		t.Errorf("prod aggregates = %+v, want requests 1, total 110", prod)
	}
}

// TestSQLiteAggregatesRespectDataFilters exercises the shared filter path:
// model/provider/label filters must shape every aggregate the same way they
// shape the request log.
func TestSQLiteAggregatesRespectDataFilters(t *testing.T) {
	reader := newLabelledSQLiteReader(t)
	ctx := context.Background()

	// Label filter narrows the summary to the single "prod" entry.
	summary, err := reader.GetSummary(ctx, UsageQueryParams{Label: "prod"})
	if err != nil {
		t.Fatalf("GetSummary() error = %v", err)
	}
	if summary.TotalRequests != 1 || summary.TotalTokens != 110 {
		t.Errorf("summary with label filter = %+v, want 1 request / 110 tokens", summary)
	}

	// Label filter narrows the by-model breakdown to entries carrying it.
	models, err := reader.GetUsageByModel(ctx, UsageQueryParams{Label: "alpha"})
	if err != nil {
		t.Fatalf("GetUsageByModel() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models with label filter = %d rows, want 2 (%#v)", len(models), models)
	}

	// Model filter narrows the by-model breakdown to that model's rows.
	models, err = reader.GetUsageByModel(ctx, UsageQueryParams{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("GetUsageByModel() error = %v", err)
	}
	if len(models) != 1 || models[0].Model != "gpt-5" || models[0].InputTokens != 1100 {
		t.Errorf("models with model filter = %#v, want single gpt-5 row with 1100 input tokens", models)
	}

	// Provider filter narrows the by-label breakdown to that provider's labels.
	labels, err := reader.GetUsageByLabel(ctx, UsageQueryParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("GetUsageByLabel() error = %v", err)
	}
	if len(labels) != 1 || labels[0].Label != "alpha" || labels[0].Requests != 1 {
		t.Errorf("labels with provider filter = %#v, want single alpha row with 1 request", labels)
	}
}

func TestSQLiteUsageLogLabelFilter(t *testing.T) {
	reader := newLabelledSQLiteReader(t)

	result, err := reader.GetUsageLog(context.Background(), UsageLogParams{UsageQueryParams: UsageQueryParams{Label: "prod"}})
	if err != nil {
		t.Fatalf("GetUsageLog() error = %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("total = %d, want 1", result.Total)
	}
	if len(result.Entries) != 1 || result.Entries[0].ID != "e1" {
		t.Fatalf("entries = %#v, want single entry e1", result.Entries)
	}

	// A label that is a substring of an existing one must not match.
	empty, err := reader.GetUsageLog(context.Background(), UsageLogParams{UsageQueryParams: UsageQueryParams{Label: "pro"}})
	if err != nil {
		t.Fatalf("GetUsageLog() error = %v", err)
	}
	if empty.Total != 0 || len(empty.Entries) != 0 {
		t.Fatalf("substring label matched: %#v", empty.Entries)
	}
}
