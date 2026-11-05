package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// QuiesceTimeout bounds how long a stopping server waits, before its graceful
// drain begins, for the connections it has already accepted to deliver their
// first request.
//
// The wait exists because of how net/http shuts down: once Shutdown has begun,
// a connection that reads a request returns without answering it, including a
// connection accepted a moment earlier whose first request was still on the
// wire. During a reload that is exactly the connection the kernel handed to the
// outgoing generation as it stopped, and its client gets an empty reply rather
// than an answer from either generation. So the server first stops accepting
// (later connections wait in the kernel's accept queue for the next
// generation), then gives what it accepted up to QuiesceTimeout to reach a
// handler, and only then shuts down; a request that has reached a handler is
// one Shutdown waits for. A client that connected and sent nothing within the
// window is cut off as before.
//
// Exported so run can check that this and GracefulDrainTimeout together fit
// inside its shutdown budget.
const QuiesceTimeout = 2 * time.Second

// quiescer tracks the connections a server has accepted but not yet seen a
// request on, and stops the server's listener from accepting more.
type quiescer struct {
	mu       sync.Mutex
	pending  map[*trackedConn]struct{}
	stopping bool
	drained  chan struct{} // closed once stopping with nothing pending

	parkOnce sync.Once
	parked   chan struct{} // closed once Accept has stopped handing out connections

	closeOnce sync.Once
	closed    chan struct{} // closed when the listener is closed
}

func newQuiescer() *quiescer {
	return &quiescer{
		pending: make(map[*trackedConn]struct{}),
		parked:  make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

// listen wraps listener so that the connections it accepts are tracked and
// quiesce can stop it from accepting without closing it.
func (q *quiescer) listen(listener net.Listener) net.Listener {
	return &quiescingListener{Listener: listener, q: q}
}

// instrument marks a connection's pending request as delivered when it
// reaches a handler of server. Call it before server starts serving.
func (q *quiescer) instrument(server *http.Server) {
	connContext := server.ConnContext
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if connContext != nil {
			ctx = connContext(ctx, conn)
		}
		return context.WithValue(ctx, trackedConnKey{}, conn)
	}
	handler := server.Handler
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, ok := r.Context().Value(trackedConnKey{}).(net.Conn); ok {
			q.settle(unwrapTrackedConn(conn))
		}
		handler.ServeHTTP(w, r)
	})
}

// quiesce stops accepting connections and waits, for at most timeout, until
// every connection already accepted has delivered its first request or
// closed. Connections that arrive meanwhile stay queued in the kernel.
func (q *quiescer) quiesce(listener net.Listener, timeout time.Duration) {
	q.mu.Lock()
	q.stopping = true
	q.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// A deadline in the past fails a pending or later Accept without closing
	// the socket, which a later generation still serves. A listener without
	// SetDeadline cannot be stopped this way, so for it quiesce only waits for
	// the connections already accepted, and one accepted after that can still
	// be dropped, as before quiescing existed. Every listener the gateway
	// serves supports it: run's boundSocket hands out TCP listeners, which
	// TestBoundSocketListenersStopAcceptingOnDeadline pins.
	if deadliner, ok := listener.(interface{ SetDeadline(time.Time) error }); ok && deadliner.SetDeadline(time.Unix(1, 0)) == nil {
		select {
		case <-q.parked:
		case <-q.closed:
		case <-timer.C:
			return
		}
	}

	select {
	case <-q.waitDrained():
	case <-q.closed:
	case <-timer.C:
	}
}

func (q *quiescer) track(conn net.Conn) net.Conn {
	tracked := &trackedConn{Conn: conn, q: q}
	q.mu.Lock()
	q.pending[tracked] = struct{}{}
	q.mu.Unlock()
	return tracked
}

// settle records that conn no longer waits on a first request: one reached a
// handler, or the connection closed.
func (q *quiescer) settle(conn *trackedConn) {
	if conn == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[conn]; !ok {
		return
	}
	delete(q.pending, conn)
	if q.stopping && len(q.pending) == 0 && q.drained != nil {
		close(q.drained)
		q.drained = nil
	}
}

func (q *quiescer) waitDrained() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		done := make(chan struct{})
		close(done)
		return done
	}
	if q.drained == nil {
		q.drained = make(chan struct{})
	}
	return q.drained
}

func (q *quiescer) isStopping() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stopping
}

type quiescingListener struct {
	net.Listener
	q *quiescer
}

// Accept hands out tracked connections until quiesce sets its deadline, then
// blocks until the listener is closed, which is how Shutdown stops Serve.
// Returning the deadline error instead would have Serve retry it in a loop.
func (l *quiescingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		return l.q.track(conn), nil
	}
	var netErr net.Error
	if l.q.isStopping() && errors.As(err, &netErr) && netErr.Timeout() {
		l.q.parkOnce.Do(func() { close(l.q.parked) })
		<-l.q.closed
		return nil, net.ErrClosed
	}
	return nil, err
}

func (l *quiescingListener) Close() error {
	l.q.closeOnce.Do(func() { close(l.q.closed) })
	return l.Listener.Close()
}

type trackedConn struct {
	net.Conn
	q *quiescer
}

func (c *trackedConn) Close() error {
	c.q.settle(c)
	return c.Conn.Close()
}

type trackedConnKey struct{}

// unwrapTrackedConn finds the tracked connection under conn, which TLS wraps.
func unwrapTrackedConn(conn net.Conn) *trackedConn {
	for conn != nil {
		if tracked, ok := conn.(*trackedConn); ok {
			return tracked
		}
		wrapper, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return nil
		}
		conn = wrapper.NetConn()
	}
	return nil
}
