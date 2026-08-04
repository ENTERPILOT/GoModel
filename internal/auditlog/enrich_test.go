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

func TestEnrichEntryWithSessionID(t *testing.T) {
	tests := []struct {
		name        string
		context     bool
		entry       *LogEntry
		sessionID   string
		wantSession string
		wantEvents  int
	}{
		{name: "nil context", sessionID: "session-1"},
		{name: "empty id", context: true, entry: &LogEntry{}, sessionID: "  "},
		{name: "missing entry", context: true, sessionID: "session-1"},
		{name: "unchanged", context: true, entry: &LogEntry{SessionID: "session-1"}, sessionID: "session-1", wantSession: "session-1"},
		{name: "trim and publish", context: true, entry: &LogEntry{}, sessionID: " session-1 ", wantSession: "session-1", wantEvents: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c *echo.Context
			publisher := &sessionUpdatePublisher{}
			if tc.context {
				e := echo.New()
				c = e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), httptest.NewRecorder())
				if tc.entry != nil {
					c.Set(string(LogEntryKey), tc.entry)
				}
				c.Set(string(LogEntryLivePublisherKey), publisher)
			}

			EnrichEntryWithSessionID(c, tc.sessionID)
			if tc.entry != nil && tc.entry.SessionID != tc.wantSession {
				t.Fatalf("session id = %q, want %q", tc.entry.SessionID, tc.wantSession)
			}
			if len(publisher.events) != tc.wantEvents {
				t.Fatalf("events = %v, want %d", publisher.events, tc.wantEvents)
			}
			if tc.wantEvents == 1 && publisher.events[0] != LiveEventAuditUpdated {
				t.Fatalf("event = %q, want %q", publisher.events[0], LiveEventAuditUpdated)
			}
		})
	}
}
