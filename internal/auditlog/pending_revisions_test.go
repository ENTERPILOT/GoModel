package auditlog

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

type entryLogger struct {
	cfg     Config
	entries []*LogEntry
}

func (l *entryLogger) Write(entry *LogEntry) { l.entries = append(l.entries, entry) }
func (l *entryLogger) Config() Config        { return l.cfg }
func (l *entryLogger) Close() error          { return nil }

func TestCompleteRequestRevisionsAppendsPendingWorkInSequence(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &LogEntry{Data: &LogData{}}
	c.Set(string(LogEntryKey), entry)
	EnrichEntryWithRequestRevision(c, RequestRevisionSnapshot{Rewriter: "compress"})
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
		return []RequestRevisionSnapshot{{Rewriter: "first"}, {Rewriter: "second"}}
	})
	require.Len(t, entry.Data.RequestRevisions, 1)

	entry.CompleteRequestRevisions()
	entry.CompleteRequestRevisions()
	revisions := entry.Data.RequestRevisions
	require.Len(t, revisions, 3)
	require.Equal(t, "first", revisions[1].Rewriter)
	require.Equal(t, 2, revisions[1].Seq)
	require.Equal(t, "second", revisions[2].Rewriter)
	require.Equal(t, 3, revisions[2].Seq)

	// A missing entry and a panicking computation are harmless.
	missing, _ := echotest.Post(t, "/", nil)
	EnrichEntryWithPendingRequestRevisions(missing, func() []RequestRevisionSnapshot { return nil })
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot { panic("boom") })
	entry.CompleteRequestRevisions()
	require.Len(t, entry.Data.RequestRevisions, 3)

	(*LogEntry)(nil).CompleteRequestRevisions()
}

func TestMiddlewareCompletesPendingRevisionsBeforeWrite(t *testing.T) {
	logger := &entryLogger{cfg: Config{Enabled: true}}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
			return []RequestRevisionSnapshot{{Rewriter: "guard"}}
		})
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.entries, 1)
	require.Len(t, logger.entries[0].Data.RequestRevisions, 1)
	require.Equal(t, "guard", logger.entries[0].Data.RequestRevisions[0].Rewriter)
}

func TestStreamEntryFinishesPendingRevisionsOnClose(t *testing.T) {
	logger := &entryLogger{cfg: Config{Enabled: true}}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	base := &LogEntry{Data: &LogData{}}
	c.Set(string(LogEntryKey), base)
	EnrichEntryWithRequestRevision(c, RequestRevisionSnapshot{Rewriter: "compress"})
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
		return []RequestRevisionSnapshot{{Rewriter: "guard"}}
	})

	streamEntry := CreateStreamEntry(c.Request().Context(), base)
	observer := NewStreamLogObserver(logger, streamEntry, "/v1/chat/completions")
	observer.OnStreamClose()
	require.Len(t, logger.entries, 1)

	revisions := logger.entries[0].Data.RequestRevisions
	require.Len(t, revisions, 2)
	require.Equal(t, "compress", revisions[0].Rewriter)
	require.Equal(t, "guard", revisions[1].Rewriter)
	require.Equal(t, 2, revisions[1].Seq)
}
