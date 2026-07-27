package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

func sessionTestContext(t *testing.T, path string, headers map[string]string) *echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	snapshot := core.NewRequestSnapshot(
		http.MethodPost, path, nil, nil, req.Header, "application/json", nil, false, "req-1", nil,
	)
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), snapshot)))
	return c
}

func TestSessionCaptureStampsContext(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	var got string
	handler := SessionCapture(detector)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("session id = %q, want header value", got)
	}
}

func TestSessionCaptureSkipsNonModelPaths(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)
	c := sessionTestContext(t, "/health", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	handler := SessionCapture(detector)(func(c *echo.Context) error {
		if id := core.SessionIDFromContext(c.Request().Context()); id != "" {
			t.Fatalf("session id = %q, want empty on non-model path", id)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
}

func TestSessionCaptureNilDetectorIsNoOp(t *testing.T) {
	c := sessionTestContext(t, "/v1/chat/completions", map[string]string{
		"X-Session-Id": "11111111-2222-3333-4444-555555555555",
	})

	called := false
	handler := SessionCapture(nil)(func(c *echo.Context) error {
		called = true
		if id := core.SessionIDFromContext(c.Request().Context()); id != "" {
			t.Fatalf("session id = %q, want empty with nil detector", id)
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if !called {
		t.Fatal("next handler not called")
	}
}
