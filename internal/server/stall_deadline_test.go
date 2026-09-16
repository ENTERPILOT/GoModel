package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/require"
)

// A client that opens a stream and never reads it must not hold the handler
// (and with it the upstream provider connection) for longer than the stall
// timeout. The handler here stands in for the provider relay: it pushes bytes
// until the kernel socket buffers on both ends are full and the write blocks.
func TestStreamStallTimeout_ReleasesHandlerWhenClientStopsReading(t *testing.T) {
	const stall = 300 * time.Millisecond

	handlerDone := make(chan error, 1)
	e := echo.New()
	e.Use(modelInteractionWriteDeadlineMiddleware(stall))
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().WriteHeader(http.StatusOK)
		chunk := []byte("data: " + string(make([]byte, 64*1024)) + "\n\n")
		var err error
		for err == nil {
			_, err = c.Response().Write(chunk)
			if err == nil {
				err = http.NewResponseController(c.Response()).Flush()
			}
		}
		handlerDone <- err
		return nil
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &http.Server{Handler: e, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: gateway\r\nContent-Length: 0\r\n\r\n")
	require.NoError(t, err)
	// Read the status line to prove the stream started, then stop reading.
	_, err = bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)

	select {
	case err := <-handlerDone:
		require.ErrorIs(t, err, ErrClientStall)

	case <-time.After(10 * time.Second):
		t.Fatal("handler still blocked on a client that stopped reading")
	}
}

// The stall deadline is armed per write, so a long gap with nothing to send
// (a slow provider) must not poison the next write.
func TestStreamStallTimeout_ToleratesQuietProvider(t *testing.T) {
	const stall = 100 * time.Millisecond

	e := echo.New()
	e.Use(modelInteractionWriteDeadlineMiddleware(stall))
	e.POST("/v1/chat/completions", func(c *echo.Context) error {
		c.Response().Header().Set("Content-Type", "text/event-stream")
		c.Response().WriteHeader(http.StatusOK)
		for _, chunk := range []string{"data: first\n\n", "data: second\n\n"} {
			if _, err := c.Response().Write([]byte(chunk)); err != nil {
				return err
			}
			if err := http.NewResponseController(c.Response()).Flush(); err != nil {
				return err
			}
			time.Sleep(3 * stall)
		}
		return nil
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &http.Server{Handler: e, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	resp, err := http.Post("http://"+listener.Addr().String()+"/v1/chat/completions", "application/json", nil)
	require.NoError(t, err)

	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var got string
	for {
		line, err := reader.ReadString('\n')
		got += line
		if err != nil {
			break
		}
	}
	want := "data: first\n\ndata: second\n\n"
	require.Equal(t, want, got)
}

func TestStallDeadlineWriter_ClassifiesOnlyTimeouts(t *testing.T) {
	w := newStallDeadlineWriter(nil, time.Second)
	timeout := &net.OpError{Op: "write", Err: &timeoutError{}}
	got := w.classify(timeout)
	require.ErrorIs(t, got, ErrClientStall)
	require.ErrorIs(t, got, timeout)

	reset := &net.OpError{Op: "write", Err: errors.New("connection reset by peer")}
	got = w.classify(reset)
	require.Equal(t, reset, got)
	got = w.classify(nil)
	require.NoError(t, got)
}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "i/o timeout" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return false }

// The wrappers above the stall writer (echo's Response, the audit capture)
// drop flush errors, so a stall during the final flush of a stream must be
// picked up from the stall writer itself rather than logged as a success.
func TestFlushStream_ReportsStallDuringFinalFlush(t *testing.T) {
	inner := &flushStallingWriter{ResponseRecorder: httptest.NewRecorder(), stallFrom: 2}
	stallWriter := newStallDeadlineWriter(inner, time.Second)
	res := echo.NewResponse(stallWriter, nil)
	capture := &typeAssertingCapture{ResponseWriter: res}

	err := flushStream(capture, io.NopCloser(strings.NewReader("data: last\n\n")))
	require.ErrorIs(t, err, ErrClientStall)
	got := inner.Body.String()
	require.Equal(t, "data: last\n\n", got)
	_, err = capture.Write([]byte("more"))
	require.ErrorIs(t, err, ErrClientStall)
}

// A stall on the header flush must return before the upstream is read at
// all: the request context is already cancelled by then, so the next read
// would report a cancellation and misclassify the stall as a disconnect.
func TestFlushStream_ReportsStallDuringInitialFlush(t *testing.T) {
	inner := &flushStallingWriter{ResponseRecorder: httptest.NewRecorder(), stallFrom: 1}
	stallWriter := newStallDeadlineWriter(inner, time.Second)
	res := echo.NewResponse(stallWriter, nil)

	err := flushStream(res, readerThatMustNotBeRead{t})
	require.ErrorIs(t, err, ErrClientStall)
}

type readerThatMustNotBeRead struct{ t *testing.T }

func (r readerThatMustNotBeRead) Read([]byte) (int, error) {
	r.t.Fatal("upstream read after the client stalled on the header flush")
	return 0, io.EOF
}

// The audit capture finds http.Hijacker with a direct type assertion on the
// writer beneath it, not through Unwrap. A realtime upgrade through the
// middleware must therefore still reach the connection.
func TestModelInteractionWriteDeadlineMiddleware_PreservesHijack(t *testing.T) {
	e := echo.New()
	e.Use(modelInteractionWriteDeadlineMiddleware(time.Second))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.SetResponse(&typeAssertingCapture{ResponseWriter: c.Response()})
			return next(c)
		}
	})
	e.GET("/v1/realtime", func(c *echo.Context) error {
		hijacker, ok := c.Response().(http.Hijacker)
		if !ok {
			return errors.New("response is not a hijacker")
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			return err
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\nhello")
		return rw.Flush()
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &http.Server{Handler: e, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)

	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /v1/realtime HTTP/1.1\r\nHost: gateway\r\n\r\n")
	require.NoError(t, err)

	got, err := io.ReadAll(conn)
	require.NoError(t, err)
	want := "HTTP/1.1 101 Switching Protocols\r\n\r\nhello"
	require.Equal(t, want, string(got))
}

// flushStallingWriter fails every flush from the stallFrom-th one on, the
// way a net.Conn does once its write deadline passed with the client no
// longer reading.
type flushStallingWriter struct {
	*httptest.ResponseRecorder
	stallFrom int
	flushes   int
}

func (w *flushStallingWriter) FlushError() error {
	w.flushes++
	if w.flushes < w.stallFrom {
		return nil
	}
	return &net.OpError{Op: "write", Err: &timeoutError{}}
}

// typeAssertingCapture mirrors the audit response capture: it forwards
// Flush and Hijack by asserting on its immediate inner writer.
type typeAssertingCapture struct {
	http.ResponseWriter
}

func (c *typeAssertingCapture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *typeAssertingCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := c.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (c *typeAssertingCapture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
