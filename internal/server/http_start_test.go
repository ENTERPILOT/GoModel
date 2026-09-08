package server

import (
	"context"
	"net"
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

// Leaving GracefulTimeout unset takes Echo's implicit 10s default and reports
// the cutoff through Echo's own logger as a bare "context deadline exceeded",
// which is what an operator saw on Ctrl+C while a stream was open. The drain
// window has to be the gateway's own decision, and sized against the
// application shutdown budget that has to contain it.
func TestNewGatewayStartConfig_ConfiguresGracefulDrain(t *testing.T) {
	cfg := newGatewayStartConfig(":0")

	if cfg.GracefulTimeout != GracefulDrainTimeout {
		t.Fatalf("GracefulTimeout = %v, want %v", cfg.GracefulTimeout, GracefulDrainTimeout)
	}
	if cfg.OnShutdownError == nil {
		t.Fatal("OnShutdownError = nil, want the drain cutoff reported by the gateway")
	}
	// A nil handler is Echo's signal to log it itself; ours must absorb the
	// error without panicking on the deadline it will actually be handed.
	cfg.OnShutdownError(context.DeadlineExceeded)
}

func TestModelInteractionWriteDeadlineMiddleware_ClearsDeadlineForModelRoutes(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/audio/translations"} {
		t.Run(path, func(t *testing.T) {
			e := echo.New()
			writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
			req := httptest.NewRequest(http.MethodPost, path, nil)
			c := e.NewContext(req, writer)

			handler := modelInteractionWriteDeadlineMiddleware(0)(func(c *echo.Context) error {
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
		})
	}
}

// With a stall timeout every write re-arms a deadline that far ahead, and the
// handler's return clears it again so net/http's trailing writes are not held
// to a deadline armed long before.
func TestModelInteractionWriteDeadlineMiddleware_ArmsStallDeadlinePerWrite(t *testing.T) {
	const stall = 45 * time.Second
	e := echo.New()
	writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c := e.NewContext(req, writer)

	handler := modelInteractionWriteDeadlineMiddleware(stall)(func(c *echo.Context) error {
		c.Response().WriteHeader(http.StatusOK)
		for _, chunk := range []string{"data: one\n\n", "data: two\n\n"} {
			if _, err := c.Response().Write([]byte(chunk)); err != nil {
				return err
			}
			c.Response().(http.Flusher).Flush()
		}
		return nil
	})

	before := time.Now()
	if err := handler(c); err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	after := time.Now()

	// clear, then (write + flush) x 2, then clear.
	if got := len(writer.deadlines); got != 6 {
		t.Fatalf("deadline calls = %d, want 6: %v", got, writer.deadlines)
	}
	if !writer.deadlines[0].IsZero() {
		t.Fatalf("first deadline = %v, want zero time", writer.deadlines[0])
	}
	if !writer.deadlines[5].IsZero() {
		t.Fatalf("last deadline = %v, want zero time", writer.deadlines[5])
	}
	for i, deadline := range writer.deadlines[1:5] {
		if deadline.Before(before.Add(stall)) || deadline.After(after.Add(stall)) {
			t.Fatalf("deadline[%d] = %v, want within %v of the write", i+1, deadline, stall)
		}
	}
	if got := writer.Body.String(); got != "data: one\n\ndata: two\n\n" {
		t.Fatalf("body = %q, want both chunks relayed", got)
	}
}

func TestModelInteractionWriteDeadlineMiddleware_LeavesNonModelRoutesUntouched(t *testing.T) {
	e := echo.New()
	writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	c := e.NewContext(req, writer)

	handler := modelInteractionWriteDeadlineMiddleware(time.Minute)(func(c *echo.Context) error {
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

// The gateway serves on a pre-bound listener in production, because the
// listening socket has to outlive the configuration a reload replaces. That
// path went through a bare start config once, which silently dropped the
// inbound timeouts and the drain window from every request the gateway served.
func TestNewGatewayStartConfigForListener_KeepsTheServerConfiguration(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	cfg := newGatewayStartConfigForListener(listener)

	if cfg.Listener != listener {
		t.Error("Listener = nil, want the pre-bound listener")
	}
	if cfg.GracefulTimeout != GracefulDrainTimeout {
		t.Errorf("GracefulTimeout = %v, want %v", cfg.GracefulTimeout, GracefulDrainTimeout)
	}
	if cfg.OnShutdownError == nil {
		t.Error("OnShutdownError = nil, want the drain cutoff reported by the gateway")
	}
	if cfg.BeforeServeFunc == nil {
		t.Fatal("BeforeServeFunc = nil, want the inbound server timeouts")
	}

	server := &http.Server{}
	if err := cfg.BeforeServeFunc(server); err != nil {
		t.Fatalf("BeforeServeFunc() error = %v", err)
	}
	for _, timeout := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadTimeout", server.ReadTimeout, inboundServerReadTimeout},
		{"ReadHeaderTimeout", server.ReadHeaderTimeout, inboundServerReadHeaderTimeout},
		{"WriteTimeout", server.WriteTimeout, inboundServerWriteTimeout},
	} {
		if timeout.got != timeout.want {
			t.Errorf("%s = %v, want %v", timeout.name, timeout.got, timeout.want)
		}
	}
}

// A nil listener is a caller mistake, not something to hand to Echo: it would
// bind a fresh address from the empty start config and serve there instead.
func TestStartWithListenerRejectsANilListener(t *testing.T) {
	srv := New(nil, &Config{})

	if err := srv.StartWithListener(context.Background(), nil); err == nil {
		t.Fatal("StartWithListener(nil) error = nil, want an error")
	}
}
