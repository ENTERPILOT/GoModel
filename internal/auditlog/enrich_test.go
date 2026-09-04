package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
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

func TestEnrichEntryWithGatewayError(t *testing.T) {
	upstream := core.ParseProviderError("openai", http.StatusUnauthorized, []byte(`{"error":{"message":"Incorrect API key provided"}}`), nil)
	gateway := core.NewAuthenticationError("", "invalid API key").WithCode("extension_authentication_failed")

	tests := []struct {
		name         string
		context      bool
		entry        *LogEntry
		err          *core.GatewayError
		wantType     string
		wantMessage  string
		wantCode     string
		wantProvider string
		wantEvents   int
	}{
		{name: "nil context", err: upstream},
		{name: "nil error", context: true, entry: &LogEntry{}},
		{name: "missing entry", context: true, err: upstream},
		{
			name: "upstream provider error", context: true, entry: &LogEntry{}, err: upstream,
			wantType: string(core.ErrorTypeAuthentication), wantMessage: "Incorrect API key provided",
			wantProvider: "openai", wantEvents: 1,
		},
		{
			name: "gateway error keeps provider empty", context: true,
			entry: &LogEntry{Data: &LogData{ErrorCode: "stale"}}, err: gateway,
			wantType: string(core.ErrorTypeAuthentication), wantMessage: "invalid API key",
			wantCode: "extension_authentication_failed", wantEvents: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c *echo.Context
			publisher := &sessionUpdatePublisher{}
			if tc.context {
				e := echo.New()
				c = e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
				if tc.entry != nil {
					c.Set(string(LogEntryKey), tc.entry)
				}
				c.Set(string(LogEntryLivePublisherKey), publisher)
			}

			EnrichEntryWithGatewayError(c, tc.err)
			if len(publisher.events) != tc.wantEvents {
				t.Fatalf("events = %v, want %d", publisher.events, tc.wantEvents)
			}
			if tc.entry == nil || tc.err == nil {
				return
			}
			if tc.entry.ErrorType != tc.wantType {
				t.Fatalf("ErrorType = %q, want %q", tc.entry.ErrorType, tc.wantType)
			}
			if tc.entry.Data == nil {
				t.Fatal("expected log data to be allocated")
			}
			if tc.entry.Data.ErrorMessage != tc.wantMessage {
				t.Fatalf("ErrorMessage = %q, want %q", tc.entry.Data.ErrorMessage, tc.wantMessage)
			}
			if tc.entry.Data.ErrorCode != tc.wantCode {
				t.Fatalf("ErrorCode = %q, want %q", tc.entry.Data.ErrorCode, tc.wantCode)
			}
			if tc.entry.Data.ErrorProvider != tc.wantProvider {
				t.Fatalf("ErrorProvider = %q, want %q", tc.entry.Data.ErrorProvider, tc.wantProvider)
			}
		})
	}
}

func TestHasRecordedError(t *testing.T) {
	tests := []struct {
		name  string
		entry *LogEntry
		want  bool
	}{
		{name: "no entry on the context", entry: nil, want: false},
		{name: "entry without an error", entry: &LogEntry{Data: &LogData{}}, want: false},
		{name: "entry carrying an error", entry: &LogEntry{ErrorType: "not_found_error", Data: &LogData{}}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), httptest.NewRecorder())
			if tc.entry != nil {
				c.Set(string(LogEntryKey), tc.entry)
			}
			if got := HasRecordedError(c); got != tc.want {
				t.Fatalf("HasRecordedError() = %v, want %v", got, tc.want)
			}
		})
	}
	if HasRecordedError(nil) {
		t.Fatal("HasRecordedError(nil) = true, want false")
	}
}
