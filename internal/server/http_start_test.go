package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGatewayStartConfig_AppliesTimeoutOverrides(t *testing.T) {
	cfg := newGatewayStartConfig(":0")
	require.NotNil(t, cfg.BeforeServeFunc)

	server := &http.Server{}
	err := cfg.BeforeServeFunc(server)
	require.NoError(t, err)
	require.Equal(t, inboundServerReadTimeout, server.ReadTimeout)
	require.Equal(t, inboundServerReadHeaderTimeout, server.ReadHeaderTimeout)
	require.Equal(t, inboundServerWriteTimeout, server.WriteTimeout)
}

// Leaving GracefulTimeout unset takes Echo's implicit 10s default and reports
// the cutoff through Echo's own logger as a bare "context deadline exceeded",
// which is what an operator saw on Ctrl+C while a stream was open. The drain
// window has to be the gateway's own decision, and sized against the
// application shutdown budget that has to contain it.
func TestNewGatewayStartConfig_ConfiguresGracefulDrain(t *testing.T) {
	cfg := newGatewayStartConfig(":0")

	require.Equal(t, GracefulDrainTimeout, cfg.GracefulTimeout)
	require.NotNil(t, cfg.OnShutdownError)

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
			err := handler(c)
			require.NoError(t, err)
			require.Len(t, writer.deadlines, 1)
			require.True(t, writer.deadlines[0].IsZero(), "deadline = %v, want zero time", writer.deadlines[0])
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
	err := handler(c)
	require.NoError(t, err)

	after := time.Now()
	got := // clear, then (write + flush) x 2, then clear.
		len(writer.deadlines)
	require.Equal(t, 6, got, "deadline calls = %d, want 6: %v", got, writer.deadlines)
	require.True(t, writer.deadlines[0].IsZero(), "first deadline = %v, want zero time", writer.deadlines[0])
	require.True(t, writer.deadlines[5].IsZero(), "last deadline = %v, want zero time", writer.deadlines[5])

	for i, deadline := range writer.deadlines[1:5] {
		require.False(t, deadline.Before(before.Add(stall)))
		require.False(t, deadline.After(after.Add(stall)), "deadline[%d] = %v, want within %v of the write", i+1, deadline, stall)
	}
	require.Equal(t, "data: one\n\ndata: two\n\n", writer.Body.String(), "want both chunks relayed")
}

func TestModelInteractionWriteDeadlineMiddleware_LeavesNonModelRoutesUntouched(t *testing.T) {
	e := echo.New()
	writer := &deadlineTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	c := e.NewContext(req, writer)

	handler := modelInteractionWriteDeadlineMiddleware(time.Minute)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	err := handler(c)
	require.NoError(t, err)
	require.Empty(t, writer.deadlines)
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
	require.NoError(t, err)

	defer listener.Close()

	cfg := newGatewayStartConfigForListener(listener)

	assert.Equal(t, listener, cfg.Listener)
	assert.Equal(t, GracefulDrainTimeout, cfg.GracefulTimeout)
	assert.NotNil(t, cfg.OnShutdownError)
	require.NotNil(t, cfg.BeforeServeFunc)

	server := &http.Server{}
	err = cfg.BeforeServeFunc(server)
	require.NoError(t, err)

	for _, timeout := range []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadTimeout", server.ReadTimeout, inboundServerReadTimeout},
		{"ReadHeaderTimeout", server.ReadHeaderTimeout, inboundServerReadHeaderTimeout},
		{"WriteTimeout", server.WriteTimeout, inboundServerWriteTimeout},
	} {
		assert.Equal(t, timeout.want, timeout.got, "%s = %v, want %v", timeout.name, timeout.got, timeout.want)
	}
}

// A nil listener is a caller mistake, not something to hand to Echo: it would
// bind a fresh address from the empty start config and serve there instead.
func TestStartWithListenerRejectsANilListener(t *testing.T) {
	srv := New(nil, &Config{})

	require.Error(t, srv.StartWithListener(context.Background(), nil))
}
