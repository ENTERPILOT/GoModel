// Package realtime implements a transparent, provider-agnostic websocket reverse
// proxy for OpenAI-compatible realtime (speech-to-speech) sessions. The provider
// event schema is the wire format, so frames are relayed verbatim; the gateway's
// job is credential injection, routing, and observation.
package realtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// MaxFrameBytes caps a single realtime message. OpenAI streams base64-encoded
// audio inside JSON text frames that routinely exceed coder/websocket's 32 KiB
// default read limit, so the limit is raised well above the largest expected
// event. Frames larger than this fail the session rather than truncating audio.
const MaxFrameBytes = 16 << 20 // 16 MiB

// Target describes the upstream realtime websocket to dial.
type Target struct {
	URL          string
	Headers      http.Header
	Subprotocols []string
}

// DialError wraps a failure to establish the upstream websocket. Proxy returns it
// only before the client connection is upgraded, so the caller can still write a
// normal HTTP error response.
type DialError struct{ Err error }

func (e *DialError) Error() string { return "realtime upstream dial failed: " + e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// Proxy upgrades the client request to a websocket, dials the upstream target,
// and relays frames bidirectionally until either side closes. onServerFrame, if
// non-nil, observes each upstream->client frame for usage tracking; it must be
// fast and must not block.
//
// The upstream is dialed first: if it fails, the client is not yet upgraded, so a
// *DialError is returned and the caller may write an HTTP error. Once the client
// is upgraded the connection is hijacked; Proxy then returns nil on a clean close
// or the terminal transport error (never a *DialError) for the caller to log.
func Proxy(w http.ResponseWriter, r *http.Request, target Target, onServerFrame func([]byte)) error {
	upstream, _, err := websocket.Dial(r.Context(), target.URL, &websocket.DialOptions{
		HTTPHeader:   target.Headers,
		Subprotocols: target.Subprotocols,
	})
	if err != nil {
		return &DialError{Err: err}
	}
	upstream.SetReadLimit(MaxFrameBytes)

	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: target.Subprotocols,
		// The gateway authenticates clients by bearer token, not cookies, so the
		// browser same-origin CSRF check does not apply. Auth is enforced upstream
		// of Proxy.
		InsecureSkipVerify: true,
	})
	if err != nil {
		_ = upstream.Close(websocket.StatusInternalError, "client upgrade failed")
		return nil // Accept already wrote an error response
	}
	client.SetReadLimit(MaxFrameBytes)

	return relay(r.Context(), client, upstream, onServerFrame)
}

// Heartbeat cadence. A silently dead peer (NAT timeout, power loss — no RST,
// no close frame) leaves both copy loops blocked in Read forever; the
// heartbeat is the only signal that tears such sessions down. Variables so
// tests can shrink them.
var (
	heartbeatInterval = 30 * time.Second
	heartbeatTimeout  = 10 * time.Second
)

// relay runs the two copy loops plus the heartbeat, tears everything down
// when any of them ends, and returns the terminal cause (nil for a normal
// close).
func relay(ctx context.Context, client, upstream *websocket.Conn, onServerFrame func([]byte)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 3)
	go func() { done <- copyFrames(ctx, upstream, client, nil) }()           // client -> upstream
	go func() { done <- copyFrames(ctx, client, upstream, onServerFrame) }() // upstream -> client
	go func() {
		done <- heartbeat(ctx, func(ctx context.Context) error { return pingBoth(ctx, client, upstream) })
	}()

	first := <-done
	cancel()
	closeBoth(client, upstream, first)
	<-done
	<-done

	return normalizeCloseError(first)
}

// heartbeat pings a session's peers on an interval so dead connections surface
// as ping timeouts instead of leaking the session. pingPeers pings every peer;
// pong responses are processed by the session's concurrent reads, which are
// always in flight. The relay and the sideband observer share this loop.
func heartbeat(ctx context.Context, pingPeers func(context.Context) error) error {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := pingPeers(ctx); err != nil {
				// A locally closed connection means teardown is already under
				// way and a concurrent read holds the definitive close cause;
				// wait for it instead of racing to report a secondary error.
				if errors.Is(err, net.ErrClosed) {
					<-ctx.Done()
					return ctx.Err()
				}
				return err
			}
		}
	}
}

func pingBoth(ctx context.Context, client, upstream *websocket.Conn) error {
	// Each peer gets its own full timeout budget: a slow-but-alive first ping
	// must not leave the second one a nearly expired deadline.
	if err := ping(ctx, client); err != nil {
		return fmt.Errorf("client heartbeat: %w", err)
	}
	if err := ping(ctx, upstream); err != nil {
		return fmt.Errorf("upstream heartbeat: %w", err)
	}
	return nil
}

func ping(ctx context.Context, conn *websocket.Conn) error {
	pingCtx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	return conn.Ping(pingCtx)
}

// copyFrames relays every message from src to dst, invoking tap on each payload
// before forwarding. It returns the first read or write error, which ends the
// session.
func copyFrames(ctx context.Context, dst, src *websocket.Conn, tap func([]byte)) error {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return err
		}
		if tap != nil {
			tap(data)
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return err
		}
	}
}

// closeBoth propagates the terminal cause's close code to both peers so a normal
// client hang-up is mirrored upstream and vice versa. Closing an already-closed
// connection is a no-op error and is ignored.
func closeBoth(client, upstream *websocket.Conn, cause error) {
	status := websocket.StatusNormalClosure
	reason := ""
	var ce websocket.CloseError
	if errors.As(cause, &ce) {
		status = ce.Code
		reason = ce.Reason
	}
	_ = client.Close(status, reason)
	_ = upstream.Close(status, reason)
}

// normalizeCloseError maps an expected end-of-session signal (a normal close or
// context cancellation from the peer teardown) to nil, leaving only genuine
// transport failures.
func normalizeCloseError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			return nil
		}
	}
	return err
}
