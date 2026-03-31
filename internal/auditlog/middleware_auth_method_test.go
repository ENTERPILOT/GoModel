package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestEnrichEntryWithAuthMethodTrimsAndValidatesAllowedValues(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &LogEntry{}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithAuthMethod(c, "  API_KEY  ")
	if entry.AuthMethod != AuthMethodAPIKey {
		t.Fatalf("entry auth method = %q, want %q", entry.AuthMethod, AuthMethodAPIKey)
	}

	EnrichEntryWithAuthMethod(c, "master_key")
	if entry.AuthMethod != AuthMethodMasterKey {
		t.Fatalf("entry auth method = %q, want %q", entry.AuthMethod, AuthMethodMasterKey)
	}

	EnrichEntryWithAuthMethod(c, "no_key")
	if entry.AuthMethod != AuthMethodNoKey {
		t.Fatalf("entry auth method = %q, want %q", entry.AuthMethod, AuthMethodNoKey)
	}

	EnrichEntryWithAuthMethod(c, "unknown")
	if entry.AuthMethod != "unknown" {
		t.Fatalf("entry auth method = %q, want %q", entry.AuthMethod, "unknown")
	}
}

func TestEnrichEntryWithAuthMethodIgnoresBlankAndUnsupportedValues(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &LogEntry{}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithAuthMethod(c, "   ")
	if entry.AuthMethod != "" {
		t.Fatalf("entry auth method = %q, want empty", entry.AuthMethod)
	}

	EnrichEntryWithAuthMethod(c, "oauth")
	if entry.AuthMethod != "" {
		t.Fatalf("entry auth method = %q, want empty", entry.AuthMethod)
	}
}
