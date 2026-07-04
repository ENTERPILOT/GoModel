package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/providers"
)

// runPassthroughMiddleware wires PassthroughHeaderCapture around a probe handler
// that records what the middleware stored on the request context. It returns the
// recorder so tests can inspect the HTTP response, and exposes the captured
// headers via a side channel (the probe populates a closure variable).
func runPassthroughMiddleware(t *testing.T, alias string, setup func(*http.Request)) (headers http.Header, nextCalled bool, rec *httptest.ResponseRecorder) {
	t.Helper()

	e := echo.New()
	captured := struct {
		headers    http.Header
		nextCalled bool
	}{}

	handler := func(c *echo.Context) error {
		captured.headers = providers.PassthroughHeadersFromContext(c.Request().Context())
		captured.nextCalled = true
		return c.String(http.StatusOK, "ok")
	}

	mw := PassthroughHeaderCapture(alias)
	wrapped := mw(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if setup != nil {
		setup(req)
	}
	rec = httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := wrapped(c); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	return captured.headers, captured.nextCalled, rec
}

func TestPassthroughHeaderCapture_StoresFilteredHeaders(t *testing.T) {
	headers, _, _ := runPassthroughMiddleware(t, "", func(req *http.Request) {
		req.Header.Set("X-Tenant-Id", "acme")
		req.Header.Set("X-Custom", "hello")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer should-be-blocked")
		req.Header.Set("X-Api-Key", "blocked-key")
	})

	if headers == nil {
		t.Fatal("expected headers in context, got nil")
	}
	if got := headers.Get("X-Tenant-Id"); got != "acme" {
		t.Errorf("X-Tenant-Id = %q, want %q", got, "acme")
	}
	if got := headers.Get("X-Custom"); got != "hello" {
		t.Errorf("X-Custom = %q, want %q", got, "hello")
	}
	if got := headers.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want %q", got, "application/json")
	}
}

func TestPassthroughHeaderCapture_StripsCredentials(t *testing.T) {
	headers, _, _ := runPassthroughMiddleware(t, "", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Api-Key", "k")
		req.Header.Set("X-GoModel-User-Path", "/v1/x")
		req.Header.Set("Cookie", "session=secret")
		req.Header.Set("X-Tenant-Id", "acme") // survivor
	})

	if headers == nil {
		t.Fatal("expected headers in context, got nil")
	}
	// Credentials and internal headers must not appear in the captured map.
	for _, blocked := range []string{"Authorization", "X-Api-Key", "X-GoModel-User-Path", "Cookie"} {
		if _, present := headers[blocked]; present {
			t.Errorf("blocked header %q leaked into passthrough context: %v", blocked, headers[blocked])
		}
	}
	// Non-credential header should still be present.
	if got := headers.Get("X-Tenant-Id"); got != "acme" {
		t.Errorf("X-Tenant-Id = %q, want %q", got, "acme")
	}
}

func TestPassthroughHeaderCapture_StripsConfiguredUserPathAlias(t *testing.T) {
	headers, _, _ := runPassthroughMiddleware(t, "X-Tenant-Path", func(req *http.Request) {
		req.Header.Set("X-Tenant-Path", "/v1/secret")
		req.Header.Set("X-Tenant-Id", "acme")
	})

	if headers == nil {
		t.Fatal("expected headers in context, got nil")
	}
	if _, present := headers["X-Tenant-Path"]; present {
		t.Errorf("user path alias leaked into passthrough context: %v", headers["X-Tenant-Path"])
	}
	if got := headers.Get("X-Tenant-Id"); got != "acme" {
		t.Errorf("X-Tenant-Id = %q, want %q", got, "acme")
	}
}

func TestPassthroughHeaderCapture_PassesThrough(t *testing.T) {
	_, nextCalled, rec := runPassthroughMiddleware(t, "", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Tenant-Id", "acme")
	})

	if !nextCalled {
		t.Fatal("middleware did not call next handler")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestPassthroughHeaderCapture_DoesNotMutateOriginalHeaders(t *testing.T) {
	e := echo.New()
	var observed http.Header

	handler := func(c *echo.Context) error {
		observed = c.Request().Header.Clone()
		return c.String(http.StatusOK, "ok")
	}

	mw := PassthroughHeaderCapture("")
	wrapped := mw(handler)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Tenant-Id", "acme")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := wrapped(c); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}

	// Original request headers must still carry credentials — the middleware
	// must not strip the wire-level headers, only the context copy.
	if got := observed.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("original Authorization mutated: %q", got)
	}
	if got := observed.Get("X-Tenant-Id"); got != "acme" {
		t.Errorf("original X-Tenant-Id lost: %q", got)
	}
}
