package auditlog

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/ext"
	"github.com/stretchr/testify/require"
)

func TestAuthenticationEventRecorderWritesDurableAuditEntry(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true, OnlyModelInteractions: true}}
	recorder := NewAuthenticationEventRecorder(logger)
	timestamp := time.Date(2026, 8, 7, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	recorder.RecordAuthenticationEvent(ext.AuthenticationEvent{
		Timestamp: timestamp, Type: "login", Outcome: "failure", Method: "sso",
		RequestID: "request-1", ClientIP: "192.0.2.10", HTTPMethod: "GET",
		Path:      "/sso/callback?code=secret-code&state=secret-state&id_token=secret-token&return_to=%2Fadmin%2Fdashboard",
		UserAgent: "browser", Reason: "group_denied",
	})

	require.Len(t, logger.entries, 1)

	entry := logger.entries[0]
	require.Equal(t, authenticationEventProvider, entry.Provider)
	require.Equal(t, 401, entry.StatusCode)
	require.Equal(t, "sso", entry.AuthMethod)
	require.Equal(t, "/sso/callback?code=REDACTED&id_token=REDACTED&return_to=%2Fadmin%2Fdashboard&state=REDACTED", entry.Path)
	require.Equal(t, "authentication_error", entry.ErrorType)
	require.Equal(t, "login", entry.Data.EventType)
	require.Equal(t, "group_denied", entry.Data.ErrorCode, "entry = %+v, data = %+v", entry, entry.Data)
	require.Same(t, time.UTC, entry.Timestamp.Location())
	require.True(t, entry.Timestamp.Equal(timestamp), "timestamp = %v, want %v in UTC", entry.Timestamp, timestamp)
}

func TestAuthenticationEventRecorderNoopsWhenAuditDisabled(t *testing.T) {
	logger := &capturingLogger{}
	NewAuthenticationEventRecorder(logger).RecordAuthenticationEvent(ext.AuthenticationEvent{Type: "login"})
	require.Empty(t, logger.entries)
}
