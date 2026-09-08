package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrClientStall reports that a response write on a model interaction route
// timed out because the client stopped accepting bytes: its socket buffer
// stayed full for the whole stream stall timeout.
var ErrClientStall = errors.New("client stopped reading the response")

// stallDeadlineWriter arms a fresh write deadline immediately before every
// write and flush, so one operation may block for at most stall. The deadline
// is re-armed per operation rather than measured from the previous one, so a
// quiet provider (a long reasoning pause with nothing to send) never trips
// it: only a client that is not draining its socket can. It replaces the
// server-wide WriteTimeout, an absolute deadline that model routes have to
// clear because a single response can legitimately outlive it.
type stallDeadlineWriter struct {
	http.ResponseWriter
	ctl   *http.ResponseController
	stall time.Duration
}

func newStallDeadlineWriter(w http.ResponseWriter, stall time.Duration) *stallDeadlineWriter {
	return &stallDeadlineWriter{ResponseWriter: w, ctl: http.NewResponseController(w), stall: stall}
}

func (w *stallDeadlineWriter) Write(p []byte) (int, error) {
	w.armDeadline()
	// p is relayed unchanged: the handler chose these bytes and their
	// content type, so this wrapper adds no reflection. lgtm[go/reflected-xss]
	n, err := w.ResponseWriter.Write(p)
	return n, w.classify(err)
}

// FlushError is what http.ResponseController prefers over Flush, so a stalled
// flush surfaces as an error to callers that go through the controller.
func (w *stallDeadlineWriter) FlushError() error {
	w.armDeadline()
	return w.classify(w.ctl.Flush())
}

func (w *stallDeadlineWriter) Flush() {
	_ = w.FlushError()
}

// Unwrap lets http.ResponseController reach the connection for the
// operations this wrapper does not intercept (deadlines, hijacking).
func (w *stallDeadlineWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *stallDeadlineWriter) armDeadline() {
	// A writer without deadline support (test recorders) keeps writes
	// unbounded, exactly as they were before this wrapper existed.
	_ = w.ctl.SetWriteDeadline(time.Now().Add(w.stall))
}

// classify tags a deadline expiry as a client stall. Any other write error
// (EPIPE, ECONNRESET) keeps its meaning of a client that went away.
func (w *stallDeadlineWriter) classify(err error) error {
	var netErr net.Error
	if err != nil && errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w for %s: %w", ErrClientStall, w.stall, err)
	}
	return err
}
