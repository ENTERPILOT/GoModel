package llmclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
)

// errStreamStalled marks an upstream stream that went silent for longer than
// the stream idle timeout.
var errStreamStalled = errors.New("provider stream stalled")

// withStreamIdleTimeout bounds the silence of a stream body once it has
// started (see httpclient.StreamIdleTimeout). The wait for the first bytes is
// not counted, so a model that reasons before its first token is unaffected.
func (c *Client) withStreamIdleTimeout(body io.ReadCloser) io.ReadCloser {
	timeout := httpclient.StreamIdleTimeout()
	if timeout <= 0 || body == nil {
		return body
	}
	return &idleTimeoutBody{ReadCloser: body, provider: c.config.ProviderName, timeout: timeout}
}

// idleTimeoutBody closes the upstream body when a Read waits longer than
// timeout for the provider, which unblocks that Read; it then reports the
// stall as a 504 provider error instead of the transport's close error. The
// timer runs only while a Read is blocked on the provider, so a consumer that
// is slow between reads never trips it. Close may race with Read (stream
// cancellation), so the timer is guarded.
type idleTimeoutBody struct {
	io.ReadCloser
	provider string
	timeout  time.Duration
	started  bool // owned by the reading goroutine

	mu      sync.Mutex
	timer   *time.Timer
	closed  bool
	stalled atomic.Bool
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if b.started {
		b.startWait()
	}
	n, err := b.ReadCloser.Read(p)
	b.stopWait()
	if b.stalled.Load() {
		return n, core.NewProviderError(b.provider, http.StatusGatewayTimeout,
			fmt.Sprintf("%s: no data for %s", errStreamStalled, b.timeout), errStreamStalled)
	}
	if n > 0 {
		b.started = true
	}
	return n, err
}

// startWait arms the timer for one wait on the provider.
func (b *idleTimeoutBody) startWait() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(b.timeout, b.fire)
		return
	}
	b.timer.Reset(b.timeout)
}

func (b *idleTimeoutBody) stopWait() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
	}
}

func (b *idleTimeoutBody) fire() {
	b.stalled.Store(true)
	_ = b.ReadCloser.Close()
}

func (b *idleTimeoutBody) Close() error {
	b.mu.Lock()
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	return b.ReadCloser.Close()
}
