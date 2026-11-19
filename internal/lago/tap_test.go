package lago

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// recordingLogger is the usage logger a tap wraps.
type recordingLogger struct {
	usage.NoopLogger
	entries   []*usage.UsageEntry
	publisher usage.LiveEventPublisher
	closed    int
	closeErr  error
}

func (l *recordingLogger) Write(entry *usage.UsageEntry) { l.entries = append(l.entries, entry) }

func (l *recordingLogger) SetLivePublisher(p usage.LiveEventPublisher) { l.publisher = p }

func (l *recordingLogger) Close() error {
	l.closed++
	return l.closeErr
}

type nopPublisher struct{}

func (nopPublisher) PublishUsageEvent(string, *usage.UsageEntry) {}

func TestTapRecordsEveryEntryAndFlushesOnClose(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"events":[]}`)
	inner := &recordingLogger{closeErr: errors.New("store closed with an error")}
	logger := NewTap(inner, startExporter(t, server.URL, idleTiming))

	entry := testEntry("e1")
	logger.Write(entry)
	logger.Write(nil)

	require.Len(t, inner.entries, 2, "the wrapped logger still gets every write")
	assert.Same(t, entry, inner.entries[0])
	assert.Equal(t, inner.Config(), logger.Config())

	publisher := nopPublisher{}
	setter, ok := logger.(interface {
		SetLivePublisher(usage.LiveEventPublisher)
	})
	require.True(t, ok)
	setter.SetLivePublisher(publisher)
	assert.Equal(t, publisher, inner.publisher)

	require.ErrorIs(t, logger.Close(), inner.closeErr)
	assert.Equal(t, 1, inner.closed)
	require.Equal(t, 1, capture.Count(), "Close sends what is buffered")
	assert.Len(t, sentBatch(t, capture.Last(t)), 2)
}

func TestNewTapWithoutExporterReturnsTheLogger(t *testing.T) {
	inner := &recordingLogger{}
	assert.Same(t, inner, NewTap(inner, nil))
	assert.Nil(t, NewTap(nil, nil))
}
