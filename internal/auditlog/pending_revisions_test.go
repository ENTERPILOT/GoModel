package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

type entryLogger struct {
	cfg     Config
	entries []*LogEntry
}

func (l *entryLogger) Write(entry *LogEntry) { l.entries = append(l.entries, entry) }
func (l *entryLogger) Config() Config        { return l.cfg }
func (l *entryLogger) Close() error          { return nil }

func TestCompleteRequestRevisionsAppendsPendingWorkInSequence(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
	entry := &LogEntry{Data: &LogData{}}
	c.Set(string(LogEntryKey), entry)
	EnrichEntryWithRequestRevision(c, RequestRevisionSnapshot{Rewriter: "compress"})
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
		return []RequestRevisionSnapshot{{Rewriter: "first"}, {Rewriter: "second"}}
	})
	if len(entry.Data.RequestRevisions) != 1 {
		t.Fatalf("pending work must not touch the entry before completion: %+v", entry.Data.RequestRevisions)
	}

	entry.CompleteRequestRevisions()
	entry.CompleteRequestRevisions()
	revisions := entry.Data.RequestRevisions
	if len(revisions) != 3 || revisions[1].Rewriter != "first" || revisions[1].Seq != 2 || revisions[2].Rewriter != "second" || revisions[2].Seq != 3 {
		t.Fatalf("revisions = %+v", revisions)
	}

	// A missing entry and a panicking computation are harmless.
	EnrichEntryWithPendingRequestRevisions(e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder()), func() []RequestRevisionSnapshot { return nil })
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot { panic("boom") })
	entry.CompleteRequestRevisions()
	if len(entry.Data.RequestRevisions) != 3 {
		t.Fatalf("revisions after a failed computation = %+v", entry.Data.RequestRevisions)
	}
	(*LogEntry)(nil).CompleteRequestRevisions()
}

func TestMiddlewareCompletesPendingRevisionsBeforeWrite(t *testing.T) {
	logger := &entryLogger{cfg: Config{Enabled: true}}
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
			return []RequestRevisionSnapshot{{Rewriter: "guard"}}
		})
		return c.String(http.StatusOK, "ok")
	})
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	if len(logger.entries) != 1 || len(logger.entries[0].Data.RequestRevisions) != 1 || logger.entries[0].Data.RequestRevisions[0].Rewriter != "guard" {
		t.Fatalf("written entries = %+v", logger.entries)
	}
}

func TestStreamEntryFinishesPendingRevisionsOnClose(t *testing.T) {
	logger := &entryLogger{cfg: Config{Enabled: true}}
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
	base := &LogEntry{Data: &LogData{}}
	c.Set(string(LogEntryKey), base)
	EnrichEntryWithRequestRevision(c, RequestRevisionSnapshot{Rewriter: "compress"})
	EnrichEntryWithPendingRequestRevisions(c, func() []RequestRevisionSnapshot {
		return []RequestRevisionSnapshot{{Rewriter: "guard"}}
	})

	streamEntry := CreateStreamEntry(c.Request().Context(), base)
	observer := NewStreamLogObserver(logger, streamEntry, "/v1/chat/completions")
	observer.OnStreamClose()
	if len(logger.entries) != 1 {
		t.Fatalf("written entries = %d", len(logger.entries))
	}
	revisions := logger.entries[0].Data.RequestRevisions
	if len(revisions) != 2 || revisions[0].Rewriter != "compress" || revisions[1].Rewriter != "guard" || revisions[1].Seq != 2 {
		t.Fatalf("stream entry revisions = %+v", revisions)
	}
}
