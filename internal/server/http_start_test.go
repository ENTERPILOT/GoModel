package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestConfigureGatewayHTTPServer_PreservesServerWriteTimeoutDefault(t *testing.T) {
	server := &http.Server{
		ReadTimeout:  time.Second,
		WriteTimeout: 30 * time.Second,
	}

	if err := configureGatewayHTTPServer(server); err != nil {
		t.Fatalf("configureGatewayHTTPServer() error = %v", err)
	}

	if got := server.ReadTimeout; got != inboundServerReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", got, inboundServerReadTimeout)
	}
	if got := server.ReadHeaderTimeout; got != inboundServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, inboundServerReadHeaderTimeout)
	}
	if got := server.WriteTimeout; got != inboundServerWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", got, inboundServerWriteTimeout)
	}
}

func TestNewGatewayStartConfig_AppliesTimeoutOverrides(t *testing.T) {
	cfg := newGatewayStartConfig(":0")
	if cfg.BeforeServeFunc == nil {
		t.Fatal("BeforeServeFunc = nil, want configured server overrides")
	}

	server := &http.Server{}
	if err := cfg.BeforeServeFunc(server); err != nil {
		t.Fatalf("BeforeServeFunc() error = %v", err)
	}

	if got := server.ReadTimeout; got != inboundServerReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", got, inboundServerReadTimeout)
	}
	if got := server.ReadHeaderTimeout; got != inboundServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", got, inboundServerReadHeaderTimeout)
	}
	if got := server.WriteTimeout; got != inboundServerWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", got, inboundServerWriteTimeout)
	}
}

func TestModelInteractionWriteDeadlineMiddleware_ClearsDeadlineForModelRoutes(t *testing.T) {
	e := echo.New()
	writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c := e.NewContext(req, writer)

	handler := modelInteractionWriteDeadlineMiddleware()(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if len(writer.deadlines) != 1 {
		t.Fatalf("deadline calls = %d, want 1", len(writer.deadlines))
	}
	if !writer.deadlines[0].IsZero() {
		t.Fatalf("deadline = %v, want zero time", writer.deadlines[0])
	}
}

func TestModelInteractionWriteDeadlineMiddleware_LeavesNonModelRoutesUntouched(t *testing.T) {
	e := echo.New()
	writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	c := e.NewContext(req, writer)

	handler := modelInteractionWriteDeadlineMiddleware()(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	if len(writer.deadlines) != 0 {
		t.Fatalf("deadline calls = %d, want 0", len(writer.deadlines))
	}
}

type deadlineTrackingWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineTrackingWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
