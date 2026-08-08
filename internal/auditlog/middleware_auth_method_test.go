package auditlog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestEnrichEntryWithAuthMethodTrimsAndValidatesIdentifiers(t *testing.T) {
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

	EnrichEntryWithAuthMethod(c, "  OAuth  ")
	if entry.AuthMethod != "oauth" {
		t.Fatalf("entry auth method = %q, want %q", entry.AuthMethod, "oauth")
	}
}

func TestEnrichEntryWithAuthMethodIgnoresBlankAndUnsafeValues(t *testing.T) {
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

	EnrichEntryWithAuthMethod(c, "oauth\nsecret")
	if entry.AuthMethod != "" {
		t.Fatalf("entry auth method = %q, want empty", entry.AuthMethod)
	}

	EnrichEntryWithAuthMethod(c, strings.Repeat("a", 65))
	if entry.AuthMethod != "" {
		t.Fatalf("entry auth method = %q, want empty", entry.AuthMethod)
	}
}

func TestEnrichEntryWithPrincipalIDTrimsAndPreservesExistingValue(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	entry := &LogEntry{PrincipalID: "existing"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithPrincipalID(c, "   ")
	if entry.PrincipalID != "existing" {
		t.Fatalf("blank principal replaced existing value with %q", entry.PrincipalID)
	}

	EnrichEntryWithPrincipalID(c, "  oidc:principal-1  ")
	if entry.PrincipalID != "oidc:principal-1" {
		t.Fatalf("principal ID = %q, want oidc:principal-1", entry.PrincipalID)
	}

	withoutEntry := e.NewContext(req, httptest.NewRecorder())
	EnrichEntryWithPrincipalID(withoutEntry, "ignored")
}
