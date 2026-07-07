package usage

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// fakePgxRows feeds scanPostgreSQLUsageLogEntries rows whose values are laid
// out in the reader's SELECT column order. Scan assigns via reflection and
// leaves the destination untouched for nil values, matching how pgx scans SQL
// NULL into pointer targets. A row/dest length mismatch errors so the fixtures
// must track the scan target list.
type fakePgxRows struct {
	rows [][]any
	idx  int
}

func (f *fakePgxRows) Next() bool {
	if f.idx >= len(f.rows) {
		return false
	}
	f.idx++
	return true
}

func (f *fakePgxRows) Scan(dest ...any) error {
	row := f.rows[f.idx-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan target count %d does not match fixture column count %d", len(dest), len(row))
	}
	for i, value := range row {
		if value == nil {
			continue
		}
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

// Covers the row-scanning half of the PostgreSQL usage log queries, pinning
// the rewrite-savings columns appended to the SELECT list: id, request_id,
// provider_id, timestamp, model, provider, provider_name, endpoint, user_path,
// cache_type, labels, input_tokens, output_tokens, total_tokens, input_cost,
// output_cost, total_cost, cost_source, raw_data, costs_calculation_caveat,
// rewrite_tokens_saved, rewrite_cost_saved.
func TestScanPostgreSQLUsageLogEntries_CarriesRewriteSavings(t *testing.T) {
	cost := 0.0375
	ts := time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC)
	rows := &fakePgxRows{rows: [][]any{
		{"with-savings", "req-saved", "provider-1", ts, "gpt-5", "openai", nil, "/v1/chat/completions", nil, nil, nil,
			100, 10, 110, nil, nil, nil, "", nil, "", int64(89), &cost},
		{"without-savings", "req-plain", "provider-2", ts, "gpt-5", "openai", nil, "/v1/chat/completions", nil, nil, nil,
			50, 10, 60, nil, nil, nil, "", nil, "", int64(0), nil},
	}}

	entries, err := scanPostgreSQLUsageLogEntries(rows)
	if err != nil {
		t.Fatalf("scanPostgreSQLUsageLogEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("scanPostgreSQLUsageLogEntries() returned %d entries, want 2", len(entries))
	}

	saved := entries[0]
	if saved.RewriteTokensSaved != 89 {
		t.Errorf("RewriteTokensSaved = %d, want 89", saved.RewriteTokensSaved)
	}
	if saved.RewriteCostSaved == nil || *saved.RewriteCostSaved != cost {
		t.Errorf("RewriteCostSaved = %v, want %v", saved.RewriteCostSaved, cost)
	}

	plain := entries[1]
	if plain.RewriteTokensSaved != 0 {
		t.Errorf("RewriteTokensSaved = %d, want 0", plain.RewriteTokensSaved)
	}
	if plain.RewriteCostSaved != nil {
		t.Errorf("RewriteCostSaved = %v, want nil", *plain.RewriteCostSaved)
	}
}
