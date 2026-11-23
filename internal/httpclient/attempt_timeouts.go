package httpclient

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// AttemptTimeouts overrides the client timeouts for each upstream HTTP
// attempt made with a context. A zero field inherits the client's setting.
// Operators set these per virtual model; they never come from callers.
type AttemptTimeouts struct {
	// Timeout bounds one attempt end to end, like http.Client.Timeout. One
	// longer than the client's response-header timeout extends that wait as
	// well, unless ResponseHeaderTimeout is set.
	Timeout time.Duration
	// ResponseHeaderTimeout bounds the wait for response headers, like
	// http.Transport.ResponseHeaderTimeout.
	ResponseHeaderTimeout time.Duration
}

// IsZero reports whether no override is set.
func (t AttemptTimeouts) IsZero() bool {
	return t.Timeout <= 0 && t.ResponseHeaderTimeout <= 0
}

type attemptTimeoutsKey struct{}

// WithAttemptTimeouts attaches per-attempt timeout overrides to ctx. Every
// HTTP attempt sent with the context gets a fresh budget, so retries and
// failovers are not squeezed into what an earlier attempt left over.
func WithAttemptTimeouts(ctx context.Context, timeouts AttemptTimeouts) context.Context {
	if timeouts.IsZero() {
		if _, ok := ctx.Value(attemptTimeoutsKey{}).(AttemptTimeouts); !ok {
			return ctx
		}
	}
	return context.WithValue(ctx, attemptTimeoutsKey{}, timeouts)
}

// AttemptTimeoutsFromContext returns the overrides attached to ctx.
func AttemptTimeoutsFromContext(ctx context.Context) AttemptTimeouts {
	timeouts, _ := ctx.Value(attemptTimeoutsKey{}).(AttemptTimeouts)
	return timeouts
}

// responseHeaderTimeoutOverride returns the response-header timeout an
// attempt needs when it differs from the configured one. An overall timeout
// longer than the configured header wait extends that wait too, unless a
// header override is set: a non-streaming call sends its headers only once
// the answer is ready, so the shorter limit would cut the longer attempt off.
func responseHeaderTimeoutOverride(ctx context.Context, configured time.Duration) (time.Duration, bool) {
	timeouts := AttemptTimeoutsFromContext(ctx)
	timeout := timeouts.ResponseHeaderTimeout
	if timeout <= 0 && configured > 0 && timeouts.Timeout > configured {
		timeout = timeouts.Timeout
	}
	return timeout, timeout > 0 && timeout != configured
}

// Do sends req with client, applying the request context's overall timeout
// override to this attempt only. The shared client is never mutated.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	timeout := AttemptTimeoutsFromContext(req.Context()).Timeout
	if timeout <= 0 || timeout == client.Timeout {
		return client.Do(req) // #nosec G704 -- URL comes from the configured provider.
	}
	attempt := *client
	attempt.Timeout = timeout
	return attempt.Do(req) // #nosec G704 -- Same URL, transport and redirect policy; only the timeout changes.
}

// maxHeaderTimeoutVariants caps the pooled transports kept for distinct
// response-header overrides. Values come from operator configuration, so few
// are live at once; past the cap one pool is evicted for each new value.
const maxHeaderTimeoutVariants = 16

// headerTimeoutVariants keeps one pooled transport per configured
// response-header timeout, so overridden attempts still reuse connections.
type headerTimeoutVariants struct {
	mu         sync.Mutex
	transports map[time.Duration]*http.Transport
}

func (v *headerTimeoutVariants) get(timeout time.Duration, build func() *http.Transport) *http.Transport {
	v.mu.Lock()
	defer v.mu.Unlock()
	if rt, ok := v.transports[timeout]; ok {
		return rt
	}
	if len(v.transports) >= maxHeaderTimeoutVariants {
		// Evict one pool; in-flight requests on it finish normally.
		for key, rt := range v.transports {
			rt.CloseIdleConnections()
			delete(v.transports, key)
			break
		}
	}
	if v.transports == nil {
		v.transports = make(map[time.Duration]*http.Transport)
	}
	rt := build()
	v.transports[timeout] = rt
	return rt
}

func (v *headerTimeoutVariants) closeIdleConnections() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, rt := range v.transports {
		rt.CloseIdleConnections()
	}
}
