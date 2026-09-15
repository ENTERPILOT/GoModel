package llmclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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

// idleTimeoutBody closes the upstream body when no bytes arrive for timeout,
// which unblocks a pending Read; that Read then reports the stall as a 504
// provider error instead of the transport's close error. Read and Close are
// used from one goroutine; only the timer callback runs concurrently.
type idleTimeoutBody struct {
	io.ReadCloser
	provider string
	timeout  time.Duration
	timer    *time.Timer
	stalled  atomic.Bool
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.stalled.Load() {
		return n, core.NewProviderError(b.provider, http.StatusGatewayTimeout,
			fmt.Sprintf("%s: no data for %s", errStreamStalled, b.timeout), errStreamStalled)
	}
	if n > 0 {
		b.arm()
	}
	return n, err
}

func (b *idleTimeoutBody) arm() {
	if b.timer == nil {
		b.timer = time.AfterFunc(b.timeout, b.fire)
		return
	}
	b.timer.Reset(b.timeout)
}

func (b *idleTimeoutBody) fire() {
	b.stalled.Store(true)
	_ = b.ReadCloser.Close()
}

func (b *idleTimeoutBody) Close() error {
	if b.timer != nil {
		b.timer.Stop()
	}
	return b.ReadCloser.Close()
}
