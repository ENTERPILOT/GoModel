package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingTapLogger struct {
	entries []*UsageEntry
	closed  bool
}

func (l *recordingTapLogger) Write(entry *UsageEntry) { l.entries = append(l.entries, entry) }
func (l *recordingTapLogger) Config() Config          { return Config{Enabled: true} }
func (l *recordingTapLogger) Close() error            { l.closed = true; return nil }

func TestBillableTapMarksEntriesOfNonBillableProviders(t *testing.T) {
	inner := &recordingTapLogger{}
	tap := NewBillableTap(inner, func(providerName string) bool { return providerName != "byok-openai" })

	tap.Write(&UsageEntry{ProviderName: "byok-openai", Provider: "openai"})
	// Rows written before provider names existed carry only the provider type.
	tap.Write(&UsageEntry{ProviderName: "  ", Provider: "byok-openai"})
	tap.Write(&UsageEntry{ProviderName: "openai-prod", Provider: "openai"})
	tap.Write(&UsageEntry{})
	tap.Write(nil)

	require.Len(t, inner.entries, 5)
	assert.False(t, inner.entries[0].IsBillable())
	assert.False(t, inner.entries[1].IsBillable())
	assert.True(t, inner.entries[2].IsBillable())
	assert.Nil(t, inner.entries[2].Billable, "a billable entry carries no flag")
	assert.True(t, inner.entries[3].IsBillable())
	assert.Nil(t, inner.entries[4])
	assert.True(t, tap.Config().Enabled)
	require.NoError(t, tap.Close())
	assert.True(t, inner.closed)
}

func TestNewBillableTapWithoutLookupReturnsLogger(t *testing.T) {
	inner := &recordingTapLogger{}
	assert.Same(t, LoggerInterface(inner), NewBillableTap(inner, nil))
	assert.Nil(t, NewBillableTap(nil, func(string) bool { return true }))
}

func TestUsageEntryIsBillable(t *testing.T) {
	var entry *UsageEntry
	assert.True(t, entry.IsBillable(), "nil entries are billable")
	entry = &UsageEntry{}
	assert.True(t, entry.IsBillable())
	entry.MarkNonBillable()
	assert.False(t, entry.IsBillable())
	entry.Billable = new(true)
	assert.True(t, entry.IsBillable())
}
