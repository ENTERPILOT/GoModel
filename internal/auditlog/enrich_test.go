package auditlog

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
				c, _ = echotest.Post(t, "/v1/responses", nil)
				if tc.entry != nil {
					c.Set(string(LogEntryKey), tc.entry)
				}
				c.Set(string(LogEntryLivePublisherKey), publisher)
			}

			EnrichEntryWithSessionID(c, tc.sessionID)
			if tc.entry != nil {
				require.Equal(t, tc.wantSession, tc.entry.SessionID)
			}
			require.Len(t, publisher.events, tc.wantEvents)
			if tc.wantEvents == 1 {
				require.Equal(t, LiveEventAuditUpdated, publisher.events[0])
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
				c, _ = echotest.Post(t, "/v1/chat/completions", nil)
				if tc.entry != nil {
					c.Set(string(LogEntryKey), tc.entry)
				}
				c.Set(string(LogEntryLivePublisherKey), publisher)
			}

			EnrichEntryWithGatewayError(c, tc.err)
			require.Len(t, publisher.events, tc.wantEvents)

			if tc.entry == nil || tc.err == nil {
				return
			}
			require.Equal(t, tc.wantType, tc.entry.ErrorType)
			require.NotNil(t, tc.entry.Data)
			require.Equal(t, tc.wantMessage, tc.entry.Data.ErrorMessage)
			require.Equal(t, tc.wantCode, tc.entry.Data.ErrorCode)
			require.Equal(t, tc.wantProvider, tc.entry.Data.ErrorProvider)
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
			c, _ := echotest.Post(t, "/v1/chat/completions", nil)
			if tc.entry != nil {
				c.Set(string(LogEntryKey), tc.entry)
			}
			require.Equal(t, tc.want, HasRecordedError(c))
		})
	}
	require.False(t, HasRecordedError(nil))
}
