package realtime

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// Observe dials the target websocket and consumes frames until the upstream
// closes or ctx is canceled, invoking tap on each frame. It backs usage tracking
// for WebRTC calls: their events flow over the peer connection's data channel
// and never pass through the gateway, so the gateway attaches to the call's
// sideband channel and watches for usage events itself.
//
// The relay's heartbeat cadence applies here too: a silently dead provider
// connection surfaces as a ping timeout instead of leaving the observer blocked
// in Read until the call TTL expires.
//
// A dial failure is returned as *DialError; a clean close returns nil.
func Observe(ctx context.Context, target Target, tap func([]byte)) error {
	conn, _, err := websocket.Dial(ctx, target.URL, &websocket.DialOptions{
		HTTPHeader:   target.Headers,
		Subprotocols: target.Subprotocols,
	})
	if err != nil {
		return &DialError{Err: err}
	}
	conn.SetReadLimit(MaxFrameBytes)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 2)
	go func() { done <- observeFrames(ctx, conn, tap) }()
	go func() {
		done <- heartbeat(ctx, func(ctx context.Context) error {
			if err := ping(ctx, conn); err != nil {
				return fmt.Errorf("observer heartbeat: %w", err)
			}
			return nil
		})
	}()

	first := <-done
	cancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")
	<-done

	return normalizeCloseError(first)
}

// observeFrames consumes upstream frames until the connection ends, invoking
// tap on each payload. The in-flight Read also processes the pongs the
// heartbeat waits for.
func observeFrames(ctx context.Context, conn *websocket.Conn, tap func([]byte)) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if tap != nil {
			tap(data)
		}
	}
}
