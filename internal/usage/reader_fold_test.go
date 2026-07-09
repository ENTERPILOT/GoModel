package usage

import (
	"errors"
	"testing"
)

// fakeInputSegmentRows is an in-memory inputSegmentRows for exercising
// foldInputSegments without a database.
type fakeInputSegmentRows struct {
	rows      []fakeSegmentRow
	pos       int
	scanErrAt int // 1-based row index where Scan should fail (0 = never)
	scanErr   error
	iterErr   error
}

type fakeSegmentRow struct {
	input    int
	provider string
	raw      *string
}

func (f *fakeInputSegmentRows) Next() bool {
	f.pos++
	return f.pos <= len(f.rows)
}

func (f *fakeInputSegmentRows) Scan(dest ...any) error {
	if f.scanErrAt == f.pos && f.scanErr != nil {
		return f.scanErr
	}
	r := f.rows[f.pos-1]
	if p, ok := dest[0].(*int); ok {
		*p = r.input
	}
	if p, ok := dest[1].(*string); ok {
		*p = r.provider
	}
	if p, ok := dest[2].(**string); ok {
		*p = r.raw
	}
	return nil
}

func (f *fakeInputSegmentRows) Err() error { return f.iterErr }

func TestFoldInputSegments_FoldsAndToleratesMalformedRawData(t *testing.T) {
	rows := &fakeInputSegmentRows{rows: []fakeSegmentRow{
		{input: 120, provider: "openai", raw: new(`{"prompt_cached_tokens":80}`)},                                       // subset → uncached 40, cached 80
		{input: 50, provider: "anthropic", raw: new(`{"cache_read_input_tokens":90,"cache_creation_input_tokens":30}`)}, // split → uncached 50, cached 90, write 30
		{input: 70, provider: "openai", raw: new(`{bad json`)},                                                          // malformed → degrades to no cache → uncached 70
		{input: 10, provider: "openai", raw: nil},                                                                       // nil raw → uncached 10
	}}

	summary := &UsageSummary{}
	if err := foldInputSegments(rows, summary); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.UncachedInputTokens != 40+50+70+10 {
		t.Fatalf("UncachedInputTokens = %d, want %d", summary.UncachedInputTokens, 40+50+70+10)
	}
	if summary.CachedInputTokens != 80+90 {
		t.Fatalf("CachedInputTokens = %d, want %d", summary.CachedInputTokens, 80+90)
	}
	if summary.CacheWriteInputTokens != 30 {
		t.Fatalf("CacheWriteInputTokens = %d, want 30", summary.CacheWriteInputTokens)
	}
}

func TestFoldInputSegments_ScanErrorPropagates(t *testing.T) {
	wantErr := errors.New("scan boom")
	rows := &fakeInputSegmentRows{
		rows:      []fakeSegmentRow{{input: 10, provider: "openai"}},
		scanErrAt: 1,
		scanErr:   wantErr,
	}
	err := foldInputSegments(rows, &UsageSummary{})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped scan error, got %v", err)
	}
}

func TestFoldInputSegments_IterationErrorPropagates(t *testing.T) {
	wantErr := errors.New("iter boom")
	rows := &fakeInputSegmentRows{
		rows:    []fakeSegmentRow{{input: 10, provider: "openai", raw: new(`{}`)}},
		iterErr: wantErr,
	}
	err := foldInputSegments(rows, &UsageSummary{})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped iteration error, got %v", err)
	}
}
