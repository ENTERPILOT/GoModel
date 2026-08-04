package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

type sessionUpdatePublisher struct {
	events []string
}

func (p *sessionUpdatePublisher) PublishLiveEvent(eventType string, _ *LogEntry) {
	p.events = append(p.events, eventType)
}

func TestEnrichEntryWithSessionIDPublishesChangedSession(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), httptest.NewRecorder())
	entry := &LogEntry{ID: "log-1"}
	publisher := &sessionUpdatePublisher{}
	c.Set(string(LogEntryKey), entry)
	c.Set(string(LogEntryLivePublisherKey), publisher)

	EnrichEntryWithSessionID(c, " session-1 ")
	if entry.SessionID != "session-1" {
		t.Fatalf("session id = %q, want session-1", entry.SessionID)
	}
	if len(publisher.events) != 1 || publisher.events[0] != LiveEventAuditUpdated {
		t.Fatalf("events = %v, want one audit.updated", publisher.events)
	}

	EnrichEntryWithSessionID(c, "session-1")
	EnrichEntryWithSessionID(c, "")
	if len(publisher.events) != 1 {
		t.Fatalf("unchanged session published extra events: %v", publisher.events)
	}
}
