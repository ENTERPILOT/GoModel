package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Bodies over the 64 KiB ingress capture limit (or chunked requests) are not
// on the snapshot when SessionCapture runs; chat/responses requests must
// materialize the body so body signals and content detection still work.
func TestSessionCaptureMaterializesLargeBodies(t *testing.T) {
	detector := session.NewDetector(session.BuiltinRules(), true)

	padding := strings.Repeat("x", 80*1024)
	body := `{"model":"gpt-4o","session_id":"big-body-session","messages":[{"role":"user","content":"` + padding + `"}]}`

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Ingress declined the body (over the inline capture limit).
	snapshot := core.NewRequestSnapshot(
		http.MethodPost, "/v1/chat/completions", nil, nil, req.Header,
		"application/json", nil, true, "req-1", nil,
	)
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), snapshot)))

	var got string
	handler := SessionCapture(detector)(func(c *echo.Context) error {
		got = core.SessionIDFromContext(c.Request().Context())
		// The handler must still be able to read the full body afterwards.
		remaining, err := io.ReadAll(c.Request().Body)
		if err != nil {
			t.Fatalf("body read after capture: %v", err)
		}
		if len(remaining) != len(body) {
			t.Fatalf("body truncated after capture: %d != %d", len(remaining), len(body))
		}
		return nil
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "big-body-session" {
		t.Fatalf("session id = %q, want body signal from a large body", got)
	}
}
