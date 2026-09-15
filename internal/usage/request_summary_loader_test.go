package usage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
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
	got, err := SummarizeUsageForRequestIDs(ctx, nil, []string{"r1"})
	require.Nil(t, got)
	require.NoError(t, err)
	got, err = SummarizeUsageForRequestIDs(ctx, &fakeUsageLoader{}, nil)
	require.Nil(t, got)
	require.NoError(t, err)

	loadErr := errors.New("reader down")
	_, err = SummarizeUsageForRequestIDs(ctx, &fakeUsageLoader{err: loadErr}, []string{"r1"})
	require.ErrorIs(t, err, loadErr)

	loader := &fakeUsageLoader{entries: map[string][]UsageLogEntry{
		"r1": {{InputTokens: 10, OutputTokens: 5}},
	}}
	got, err = SummarizeUsageForRequestIDs(ctx, loader, []string{"r1"})
	require.NoError(t, err)

	summary := got["r1"]
	require.NotNil(t, summary)
	require.Equal(t, int64(10), summary.InputTokens)
	require.Equal(t, int64(5), summary.OutputTokens)
	require.Equal(t, int64(15), summary.TotalTokens)
}
