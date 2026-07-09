package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestObserveTapsFramesUntilClose verifies the sideband observer consumes every
// upstream frame and returns nil on a normal close.
func TestObserveTapsFramesUntilClose(t *testing.T) {
	frames := []string{
		`{"type":"session.created"}`,
		`{"type":"response.done","response":{"usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for _, frame := range frames {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
				return
			}
		}
		conn.Close(websocket.StatusNormalClosure, "call ended")
	}))
	defer upstream.Close()

	var seen []string
	target := Target{
		URL:     "ws" + strings.TrimPrefix(upstream.URL, "http"),
		Headers: http.Header{"Authorization": {"Bearer observer-key"}},
	}
	err := Observe(context.Background(), target, func(frame []byte) {
		seen = append(seen, string(frame))
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != len(frames) {
		t.Fatalf("tapped %d frames, want %d", len(seen), len(frames))
	}
	for i := range frames {
		if seen[i] != frames[i] {
			t.Errorf("frame %d = %q, want %q", i, seen[i], frames[i])
		}
	}
	if gotAuth != "Bearer observer-key" {
		t.Errorf("upstream saw Authorization %q, want injected observer credentials", gotAuth)
	}
}

func TestObserveReturnsDialError(t *testing.T) {
	err := Observe(context.Background(), Target{URL: "ws://127.0.0.1:1/v1/realtime"}, nil)
	var de *DialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DialError", err)
	}
}

func TestObserveDetectsDeadUpstream(t *testing.T) {
	restore := SetHeartbeatCadenceForTest(30*time.Millisecond, 30*time.Millisecond)
	defer restore()

	// A server that completes the handshake and then goes silent without ever
	// reading cannot answer pings — exactly like a peer that lost power. The
	// heartbeat must tear the observer down long before the outer context cap.
	hold := make(chan struct{})
	defer close(hold)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		<-hold
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Observe(ctx, Target{URL: "ws" + strings.TrimPrefix(upstream.URL, "http")}, nil)
	if err == nil {
		t.Fatal("expected a heartbeat failure for a dead upstream")
	}
	if ctx.Err() != nil {
		t.Fatal("observer ended only via the outer context; the heartbeat did not fire")
	}
	if !strings.Contains(err.Error(), "observer heartbeat") {
		t.Errorf("err = %v, want a heartbeat-attributed failure", err)
	}
}

func TestObserveStopsOnContextCancel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Hold the connection open without sending anything.
		<-r.Context().Done()
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := Observe(ctx, Target{URL: "ws" + strings.TrimPrefix(upstream.URL, "http")}, nil)
	if err == nil {
		return // normalized cancellation is acceptable
	}
	var de *DialError
	if errors.As(err, &de) {
		t.Fatalf("err = %v, want a post-dial termination, not a dial error", err)
	}
}
