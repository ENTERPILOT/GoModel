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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.Len(t, seen, len(frames))

	for i := range frames {
		assert.Equal(t, frames[i], seen[i], "frame %d = %q, want %q", i, seen[i], frames[i])
	}
	assert.Equal(t, "Bearer observer-key", gotAuth)
}

func TestObserveReturnsDialError(t *testing.T) {
	err := Observe(context.Background(), Target{URL: "ws://127.0.0.1:1/v1/realtime"}, nil)
	_, ok := errors.AsType[*DialError](err)
	require.True(t, ok)
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
	require.Error(t, err)
	require.NoError(t, ctx.Err())
	assert.Contains(t, err.Error(), "observer heartbeat")
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
	_, ok := errors.AsType[*DialError](err)
	require.False(t, ok)
}
