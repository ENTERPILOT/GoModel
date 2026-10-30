package httpclient

import (
	"context"
	"io"
	"net/http"
	"time"
)

// RequestTimeouts overrides upstream timeouts independently; nil inherits and zero disables.
type RequestTimeouts struct {
	Timeout               *time.Duration
	ResponseHeaderTimeout *time.Duration
}

type requestTimeoutsKey struct{}

// WithRequestTimeouts applies overrides to every HTTP attempt using this context.
func WithRequestTimeouts(ctx context.Context, overrides RequestTimeouts) context.Context {
	return context.WithValue(ctx, requestTimeoutsKey{}, overrides)
}

// Do applies the request's overall timeout without mutating the shared client.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	overrides, _ := req.Context().Value(requestTimeoutsKey{}).(RequestTimeouts)
	if overrides.Timeout == nil {
		return client.Do(req) // #nosec G704 -- URL comes from the configured provider; timeout overrides never alter it or redirect policy.
	}
	copy := *client
	copy.Timeout = *overrides.Timeout
	return copy.Do(req) // #nosec G704 -- Preserve the provider URL, transport guards and redirect policy; only the timeout changes.
}

type requestTimeoutTransport struct{ base *http.Transport }

func (t *requestTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	overrides, _ := req.Context().Value(requestTimeoutsKey{}).(RequestTimeouts)
	if overrides.ResponseHeaderTimeout == nil || *overrides.ResponseHeaderTimeout == t.base.ResponseHeaderTimeout {
		return t.base.RoundTrip(req)
	}
	// A request-owned transport avoids mutating shared settings or retaining an
	// unbounded pool of transports for caller-selected timeout values.
	transport := t.base.Clone()
	transport.ResponseHeaderTimeout = *overrides.ResponseHeaderTimeout
	resp, err := transport.RoundTrip(req)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	body := &timeoutResponseBody{ReadCloser: resp.Body, transport: transport}
	if writer, ok := resp.Body.(io.Writer); ok {
		// Switching Protocols bodies must remain writable for WebSocket clients.
		resp.Body = &timeoutReadWriteBody{timeoutResponseBody: body, Writer: writer}
	} else {
		resp.Body = body
	}
	return resp, nil
}

func (t *requestTimeoutTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func (t *requestTimeoutTransport) Unwrap() http.RoundTripper { return t.base }

type timeoutResponseBody struct {
	io.ReadCloser
	transport *http.Transport
}

type timeoutReadWriteBody struct {
	*timeoutResponseBody
	io.Writer
}

func (b *timeoutResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}
