package realtime

import (
	"context"

	"github.com/coder/websocket"
)

// Observe dials the target websocket and consumes frames until the upstream
// closes or ctx is canceled, invoking tap on each frame. It backs usage tracking
// for WebRTC calls: their events flow over the peer connection's data channel
// and never pass through the gateway, so the gateway attaches to the call's
// sideband channel and watches for usage events itself.
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
	defer conn.Close(websocket.StatusNormalClosure, "")

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return normalizeCloseError(err)
		}
		if tap != nil {
			tap(data)
		}
	}
}
