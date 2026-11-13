package usagemetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/usage"
)

type recordingLogger struct {
	entries   []*usage.UsageEntry
	publisher usage.LiveEventPublisher
	closed    bool
}

func (l *recordingLogger) Write(entry *usage.UsageEntry) { l.entries = append(l.entries, entry) }
func (l *recordingLogger) Config() usage.Config          { return usage.Config{Enabled: true} }
func (l *recordingLogger) Close() error                  { l.closed = true; return nil }

func (l *recordingLogger) SetLivePublisher(p usage.LiveEventPublisher) { l.publisher = p }

type sampleRecorder struct{ samples []Sample }

func (r *sampleRecorder) RecordUsage(s Sample) { r.samples = append(r.samples, s) }

type nopPublisher struct{}

func (nopPublisher) PublishUsageEvent(string, *usage.UsageEntry) {}

func TestTapRecordsEveryPipelineAndDelegates(t *testing.T) {
	ResetCallerValues()
	t.Cleanup(ResetCallerValues)
	inner := &recordingLogger{}
	prometheus, otel := &sampleRecorder{}, &sampleRecorder{}
	callers := NewCallers(config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 1}, nil)
	tap := NewTap(inner, callers, prometheus, otel)

	entry := &usage.UsageEntry{Provider: "openai", Model: "gpt-5", UserPath: "/team/alice", InputTokens: 10, OutputTokens: 5}
	tap.Write(entry)
	tap.Write(nil)

	want := []Sample{{
		Provider: "openai", ProviderType: "openai", Model: "gpt-5",
		Caller: Caller{UserPath: "/team"}, InputTokens: 10, OutputTokens: 5,
	}}
	assert.Equal(t, want, prometheus.samples)
	assert.Equal(t, want, otel.samples)
	require.Len(t, inner.entries, 2)
	assert.Same(t, entry, inner.entries[0])
	assert.Nil(t, inner.entries[1])
	assert.True(t, tap.Config().Enabled)
	require.NoError(t, tap.Close())
	assert.True(t, inner.closed)
}

func TestTapSkipsEntriesWithoutTokensOrCost(t *testing.T) {
	tests := []struct {
		name  string
		entry *usage.UsageEntry
	}{
		{name: "mcp tool call", entry: &usage.UsageEntry{Provider: "mcp", ProviderName: "github", Model: "search", RawData: map[string]any{"method": "tools/call"}}},
		{name: "unbilled response-cache hit", entry: &usage.UsageEntry{Provider: "openai", Model: "gpt-5", CacheType: usage.CacheTypeExact, InputTokens: 10, TotalCost: new(0.2), BilledCost: new(0.0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner, recorder := &recordingLogger{}, &sampleRecorder{}

			NewTap(inner, nil, recorder).Write(tt.entry)

			assert.Empty(t, recorder.samples)
			assert.Len(t, inner.entries, 1, "the entry is still stored")
		})
	}
}

func TestNewTapWithoutRecordersReturnsLogger(t *testing.T) {
	inner := &recordingLogger{}

	assert.Same(t, inner, NewTap(inner, nil))
	assert.Nil(t, NewTap(nil, nil, &sampleRecorder{}))
}

func TestTapForwardsLivePublisher(t *testing.T) {
	inner := &recordingLogger{}
	tap := NewTap(inner, nil, &sampleRecorder{})

	setter, ok := tap.(interface {
		SetLivePublisher(usage.LiveEventPublisher)
	})
	require.True(t, ok)
	setter.SetLivePublisher(nopPublisher{})

	assert.Equal(t, nopPublisher{}, inner.publisher)
}
