package auditlog

import (
	"testing"
	"time"

	"github.com/enterpilot/gomodel/ext"
)

func TestAuthenticationEventRecorderWritesDurableAuditEntry(t *testing.T) {
	logger := &capturingLogger{cfg: Config{Enabled: true, OnlyModelInteractions: true}}
	recorder := NewAuthenticationEventRecorder(logger)
	timestamp := time.Date(2026, 8, 7, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	recorder.RecordAuthenticationEvent(ext.AuthenticationEvent{
		Timestamp: timestamp, Type: "login", Outcome: "failure", Method: "sso",
		RequestID: "request-1", ClientIP: "192.0.2.10", HTTPMethod: "GET",
		Path: "/sso/callback", UserAgent: "browser", Reason: "group_denied",
	})

	if len(logger.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Provider != authenticationEventProvider || entry.StatusCode != 401 ||
		entry.AuthMethod != "sso" || entry.Path != "/sso/callback" ||
		entry.ErrorType != "authentication_error" || entry.Data.EventType != "login" ||
		entry.Data.ErrorCode != "group_denied" {
		t.Fatalf("entry = %+v, data = %+v", entry, entry.Data)
	}
	if entry.Timestamp.Location() != time.UTC || !entry.Timestamp.Equal(timestamp) {
		t.Fatalf("timestamp = %v, want %v in UTC", entry.Timestamp, timestamp)
	}
}

func TestAuthenticationEventRecorderNoopsWhenAuditDisabled(t *testing.T) {
	logger := &capturingLogger{}
	NewAuthenticationEventRecorder(logger).RecordAuthenticationEvent(ext.AuthenticationEvent{Type: "login"})
	if len(logger.entries) != 0 {
		t.Fatalf("entries = %d, want none", len(logger.entries))
	}
}
