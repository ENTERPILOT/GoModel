package markups

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/usage"
)

type recordingLogger struct {
	entries []*usage.UsageEntry
	closed  bool
}

func (l *recordingLogger) Write(entry *usage.UsageEntry) { l.entries = append(l.entries, entry) }
func (l *recordingLogger) Config() usage.Config          { return usage.Config{Enabled: true} }
func (l *recordingLogger) Close() error                  { l.closed = true; return nil }

func TestUsageTapStampsBilledCostAndDelegates(t *testing.T) {
	svc, err := New(config.PricingConfig{Markups: map[string]float64{"openai/": 25}}, testCatalog)
	require.NoError(t, err)
	inner := &recordingLogger{}
	tap := NewUsageTap(inner, svc)

	tap.Write(&usage.UsageEntry{ProviderName: "openai", Provider: "openai", Model: "gpt-5", TotalCost: new(4.0)})
	tap.Write(nil)

	require.Len(t, inner.entries, 2)
	require.NotNil(t, inner.entries[0].BilledCost)
	require.InDelta(t, 5.0, *inner.entries[0].BilledCost, 1e-12)
	require.Nil(t, inner.entries[1])
	require.True(t, tap.Config().Enabled)
	require.NoError(t, tap.Close())
	require.True(t, inner.closed)
}

func TestNewUsageTapWithoutServiceReturnsLogger(t *testing.T) {
	inner := &recordingLogger{}
	require.Same(t, inner, NewUsageTap(inner, nil))
}

type publishingLogger struct {
	recordingLogger
	publisher usage.LiveEventPublisher
}

func (l *publishingLogger) SetLivePublisher(p usage.LiveEventPublisher) { l.publisher = p }

type nopPublisher struct{}

func (nopPublisher) PublishUsageEvent(string, *usage.UsageEntry) {}

func TestUsageTapForwardsLivePublisher(t *testing.T) {
	svc, err := New(config.PricingConfig{}, testCatalog)
	require.NoError(t, err)
	inner := &publishingLogger{}
	tap := NewUsageTap(inner, svc)

	publisherSetter, ok := tap.(interface {
		SetLivePublisher(usage.LiveEventPublisher)
	})
	require.True(t, ok, "tap must expose SetLivePublisher so live dashboard events keep flowing")
	publisherSetter.SetLivePublisher(nopPublisher{})
	assert.Equal(t, nopPublisher{}, inner.publisher)

	// A wrapped logger without live support is left alone.
	NewUsageTap(&recordingLogger{}, svc).(*UsageTap).SetLivePublisher(nopPublisher{})
}
