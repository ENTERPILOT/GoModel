// Package realtime implements a provider-agnostic websocket reverse proxy for
// OpenAI-compatible realtime (speech-to-speech) sessions. For most providers the
// event schema is the wire format, so frames are relayed verbatim and the
// gateway's job is credential injection, routing, and observation. A provider
// with its own websocket protocol supplies a Translator, and the relay converts
// each frame through it instead.
package realtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/enterpilot/gomodel/internal/httpclient"
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
	// HTTPClient carries the provider's outbound proxy and TLS trust for the
	// dial. Nil uses the shared default client, which still applies the
	// gateway's TLS trust and the process proxy environment.
	HTTPClient *http.Client
}

// dialOptions builds the websocket dial options for target.
func (target Target) dialOptions() *websocket.DialOptions {
	client := target.HTTPClient
	if client == nil {
		client = httpclient.NewDefaultHTTPClient()
	}
	return &websocket.DialOptions{
		HTTPClient:   client,
		HTTPHeader:   target.Headers,
		Subprotocols: target.Subprotocols,
	}
}

// DialError wraps a failure to establish the upstream websocket. Proxy returns it
// only before the client connection is upgraded, so the caller can still write a
// normal HTTP error response.
type DialError struct{ Err error }

func (e *DialError) Error() string { return "realtime upstream dial failed: " + e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// Hooks observe and shape the frames a session relays. Every hook is optional
// and runs inline on the relay path, so each must be fast and must not block.
type Hooks struct {
	// OnClientFrame observes each client->upstream frame. It backs metering of
	// what the client sends, such as input audio for sessions the provider bills
	// by duration.
	OnClientFrame func([]byte)
	// MapClientFrame rewrites each client->upstream frame before it is
	// forwarded, returning the frame unchanged when it has nothing to do. It
	// carries session policy such as pinning the transcription model.
	MapClientFrame func([]byte) []byte
	// OnServerFrame observes each upstream->client frame. It backs usage
	// tracking, which reads the provider's usage events. In a translated
	// session it observes the translated frames, the ones the client receives.
	OnServerFrame func([]byte)
	// Translator converts the session between the client's event schema and
	// the upstream's own protocol. Nil relays frames verbatim. The client-frame
	// hooks run before it, so they always see what the client sent.
	Translator Translator
}

// Translator converts the frames of one session between the client's event
// schema and the upstream's protocol. The two methods run on different
// goroutines.
type Translator interface {
	// ClientEvent translates one client frame into the frames to send upstream
	// and the frames to answer the client with directly.
	ClientEvent(frame []byte) (upstream, replies [][]byte)
	// ProviderEvent translates one upstream frame into the frames the client
	// receives.
	ProviderEvent(frame []byte) [][]byte
}

// clientFrames folds the client-frame observer and mapper into the single
// transform the client->upstream copy loop applies.
func (h Hooks) clientFrames() func([]byte) []byte {
	if h.OnClientFrame == nil {
		return h.MapClientFrame
	}
	return func(frame []byte) []byte {
		h.OnClientFrame(frame)
		if h.MapClientFrame == nil {
			return frame
		}
		return h.MapClientFrame(frame)
	}
}

// Proxy upgrades the client request to a websocket, dials the upstream target,
// and relays frames bidirectionally until either side closes, running hooks on
// the frames as they pass.
//
// The upstream is dialed first: if it fails, the client is not yet upgraded, so a
// *DialError is returned and the caller may write an HTTP error. Once the client
// is upgraded the connection is hijacked; Proxy then returns nil on a clean close
// or the terminal transport error (never a *DialError) for the caller to log.
func Proxy(w http.ResponseWriter, r *http.Request, target Target, hooks Hooks) error {
	upstream, _, err := websocket.Dial(r.Context(), target.URL, target.dialOptions())
	if err != nil {
		return &DialError{Err: err}
	}
	upstream.SetReadLimit(MaxFrameBytes)

	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: target.Subprotocols,
		// The library's origin check would run here, after the upstream is
		// already dialed. The caller checks Origin before it calls Proxy
		// (server.realtimeOriginGuard), where a refusal costs nothing.
		InsecureSkipVerify: true,
	})
	if err != nil {
		_ = upstream.Close(websocket.StatusInternalError, "client upgrade failed")
		return nil // Accept already wrote an error response
	}
	client.SetReadLimit(MaxFrameBytes)

	return relay(r.Context(), client, upstream, hooks)
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
func relay(ctx context.Context, client, upstream *websocket.Conn, hooks Hooks) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 3)
	if hooks.Translator == nil {
		go func() { done <- copyFrames(ctx, upstream, client, hooks.clientFrames()) }()         // client -> upstream
		go func() { done <- copyFrames(ctx, client, upstream, observe(hooks.OnServerFrame)) }() // upstream -> client
	} else {
		go func() { done <- translateClientFrames(ctx, upstream, client, hooks) }()
		go func() { done <- translateUpstreamFrames(ctx, client, upstream, hooks) }()
	}
	go func() {
		done <- heartbeat(ctx, func(ctx context.Context) error { return pingBoth(ctx, client, upstream) })
	}()

	first := <-done
	// A Read whose context ends drops its connection without a close frame, so
	// a peer's close is mirrored before cancelling or its code could be lost.
	// Any other failure cancels first: a dead peer would stall the handshake.
	if _, closed := errors.AsType[websocket.CloseError](first); !closed {
		cancel()
	}
	closeBoth(client, upstream, first)
	cancel()
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

// copyFrames relays every message from src to dst, passing each payload through
// mapFrame before forwarding. It returns the first read or write error, which
// ends the session.
func copyFrames(ctx context.Context, dst, src *websocket.Conn, mapFrame func([]byte) []byte) error {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return err
		}
		if mapFrame != nil {
			data = mapFrame(data)
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return err
		}
	}
}

// translateClientFrames is the client->upstream loop of a translated session.
// Each client frame passes the client-frame hooks, then the translator; what it
// returns for the upstream is forwarded and what it answers itself goes back to
// the client. Translated frames are JSON events, so they travel as text.
func translateClientFrames(ctx context.Context, upstream, client *websocket.Conn, hooks Hooks) error {
	mapFrame := hooks.clientFrames()
	for {
		_, data, err := client.Read(ctx)
		if err != nil {
			return err
		}
		if mapFrame != nil {
			data = mapFrame(data)
		}
		forward, replies := hooks.Translator.ClientEvent(data)
		if err := writeFrames(ctx, upstream, forward); err != nil {
			return err
		}
		if err := writeFrames(ctx, client, replies); err != nil {
			return err
		}
	}
}

// translateUpstreamFrames is the upstream->client loop of a translated session.
// The server-frame tap observes the translated frames, because those carry the
// event schema it reads.
func translateUpstreamFrames(ctx context.Context, client, upstream *websocket.Conn, hooks Hooks) error {
	for {
		_, data, err := upstream.Read(ctx)
		if err != nil {
			return err
		}
		frames := hooks.Translator.ProviderEvent(data)
		if hooks.OnServerFrame != nil {
			for _, frame := range frames {
				hooks.OnServerFrame(frame)
			}
		}
		if err := writeFrames(ctx, client, frames); err != nil {
			return err
		}
	}
}

// writeFrames writes each frame as its own text message. Both loops of a
// translated session write to the client; the connection serializes them.
func writeFrames(ctx context.Context, dst *websocket.Conn, frames [][]byte) error {
	for _, frame := range frames {
		if err := dst.Write(ctx, websocket.MessageText, frame); err != nil {
			return err
		}
	}
	return nil
}

// observe adapts a read-only frame tap to copyFrames' mapping shape.
func observe(tap func([]byte)) func([]byte) []byte {
	if tap == nil {
		return nil
	}
	return func(frame []byte) []byte {
		tap(frame)
		return frame
	}
}

// closeBoth propagates the terminal cause's close code to both peers so a normal
// client hang-up is mirrored upstream and vice versa. Closing an already-closed
// connection is a no-op error and is ignored.
func closeBoth(client, upstream *websocket.Conn, cause error) {
	status := websocket.StatusNormalClosure
	reason := ""
	if ce, ok := errors.AsType[websocket.CloseError](cause); ok {
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
	if ce, ok := errors.AsType[websocket.CloseError](err); ok {
		switch ce.Code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			return nil
		}
	}
	return err
}
