package server

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
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
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: e, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: gateway\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	// Read the status line to prove the stream started, then stop reading.
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("read status line: %v", err)
	}

	select {
	case err := <-handlerDone:
		if !errors.Is(err, ErrClientStall) {
			t.Fatalf("handler error = %v, want ErrClientStall", err)
		}
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
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: e, WriteTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	resp, err := http.Post("http://"+listener.Addr().String()+"/v1/chat/completions", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
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
	if want := "data: first\n\ndata: second\n\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestStallDeadlineWriter_ClassifiesOnlyTimeouts(t *testing.T) {
	w := newStallDeadlineWriter(nil, time.Second)
	timeout := &net.OpError{Op: "write", Err: &timeoutError{}}
	if got := w.classify(timeout); !errors.Is(got, ErrClientStall) || !errors.Is(got, timeout) {
		t.Fatalf("classify(timeout) = %v, want ErrClientStall wrapping the cause", got)
	}
	reset := &net.OpError{Op: "write", Err: errors.New("connection reset by peer")}
	if got := w.classify(reset); got != reset {
		t.Fatalf("classify(reset) = %v, want the error unchanged", got)
	}
	if got := w.classify(nil); got != nil {
		t.Fatalf("classify(nil) = %v, want nil", got)
	}
}

type timeoutError struct{}

func (*timeoutError) Error() string   { return "i/o timeout" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return false }
