package usage

import (
	"context"
	"errors"
	"testing"
)

type fakeUsageLoader struct {
	entries map[string][]UsageLogEntry
	err     error
}

func (f *fakeUsageLoader) GetUsageByRequestIDs(context.Context, []string) (map[string][]UsageLogEntry, error) {
	return f.entries, f.err
}

func TestSummarizeUsageForRequestIDs(t *testing.T) {
	ctx := context.Background()

	if got, err := SummarizeUsageForRequestIDs(ctx, nil, []string{"r1"}); got != nil || err != nil {
		t.Fatalf("nil reader = (%v, %v), want nil, nil", got, err)
	}
	if got, err := SummarizeUsageForRequestIDs(ctx, &fakeUsageLoader{}, nil); got != nil || err != nil {
		t.Fatalf("empty ids = (%v, %v), want nil, nil", got, err)
	}

	loadErr := errors.New("reader down")
	if _, err := SummarizeUsageForRequestIDs(ctx, &fakeUsageLoader{err: loadErr}, []string{"r1"}); !errors.Is(err, loadErr) {
		t.Fatalf("error = %v, want %v", err, loadErr)
	}

	loader := &fakeUsageLoader{entries: map[string][]UsageLogEntry{
		"r1": {{InputTokens: 10, OutputTokens: 5}},
	}}
	got, err := SummarizeUsageForRequestIDs(ctx, loader, []string{"r1"})
	if err != nil {
		t.Fatalf("SummarizeUsageForRequestIDs() error = %v", err)
	}
	summary := got["r1"]
	if summary == nil || summary.InputTokens != 10 || summary.OutputTokens != 5 || summary.TotalTokens != 15 {
		t.Fatalf("summary = %+v, want 10 input / 5 output / 15 total", summary)
	}
}
