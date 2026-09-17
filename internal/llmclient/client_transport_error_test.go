package llmclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// A transport error must never hand the client the upstream URL: it discloses
// internal topology and would leak a token embedded in a custom base_url.
func TestTransportErrorMessage(t *testing.T) {
	const upstream = "https://secret-host.internal:8443/v1/chat/completions?key=s3cret"
	wrap := func(err error) error {
		return &url.Error{Op: "Post", URL: upstream, Err: err}
	}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "timeout",
			err:  wrap(errors.New("net/http: timeout awaiting response headers")),
			want: "provider request timed out",
		},
		{
			name: "deadline exceeded",
			err:  wrap(context.DeadlineExceeded),
			want: "provider request timed out",
		},
		{
			name: "canceled",
			err:  wrap(context.Canceled),
			want: "provider request canceled",
		},
		{
			name: "connection refused",
			err:  wrap(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			want: "provider refused the connection",
		},
		{
			name: "tls handshake",
			err:  wrap(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}),
			want: "TLS handshake with provider failed",
		},
		{
			name: "certificate verification",
			err:  wrap(&tls.CertificateVerificationError{Err: errors.New("certificate signed by unknown authority")}),
			want: "TLS handshake with provider failed",
		},
		{
			name: "dns failure",
			err:  wrap(&net.DNSError{Err: "no such host", Name: "secret-host.internal", IsNotFound: true}),
			want: "provider host could not be resolved",
		},
		{
			name: "unclassified",
			err:  wrap(errors.New("connection reset by peer")),
			want: "failed to send request to provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transportErrorMessage(tt.err)
			require.Equal(t, tt.want, got)
			require.NotContains(t, got, "secret-host.internal")
			require.NotContains(t, got, "s3cret", "transportErrorMessage() = %q, must not disclose the upstream URL", got)

		})
	}
}

// The server-side log keeps the upstream URL for diagnosis, but not a
// credential an operator embedded in a custom base_url.
func TestSanitizedTransportError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		want       string
		wantAbsent string
	}{
		{
			name:       "query credential is dropped",
			err:        &url.Error{Op: "Post", URL: "https://host.internal:8443/v1/chat/completions?key=s3cret", Err: errors.New("connection refused")},
			want:       `Post "https://host.internal:8443/v1/chat/completions": connection refused`,
			wantAbsent: "s3cret",
		},
		{
			name:       "userinfo is dropped",
			err:        &url.Error{Op: "Post", URL: "https://user:p4ssw0rd@host.internal/v1/chat/completions", Err: errors.New("connection refused")},
			want:       `Post "https://host.internal/v1/chat/completions": connection refused`,
			wantAbsent: "p4ssw0rd",
		},
		{
			name: "non-URL errors pass through",
			err:  errors.New("connection reset by peer"),
			want: "connection reset by peer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizedTransportError(tt.err)
			require.Equal(t, tt.want, got)

			if tt.wantAbsent != "" {
				require.NotContains(t, got, tt.wantAbsent, "sanitizedTransportError()")
			}
		})
	}
}

func TestReadErrorMessage(t *testing.T) {
	got := readErrorMessage(context.DeadlineExceeded)
	require.Equal(t, "timed out reading provider response", got)

	got = readErrorMessage(&net.OpError{Op: "read", Net: "tcp", Source: mustAddr(t, "127.0.0.1:51423"), Err: errors.New("connection reset by peer")})
	require.Equal(t, "failed to read provider response", got)
	require.NotContains(t, got, "127.0.0.1", "readErrorMessage() = %q, must not disclose connection details", got)
}

// End to end: a refused connection keeps its 502 mapping and names the
// provider, without echoing the base URL back to the caller.
func TestClientTransportErrorHidesBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL
	server.Close() // nothing listens on the port any more

	cfg := DefaultConfig("mockB", baseURL)
	cfg.Retry.MaxRetries = 0
	client := NewWithHTTPClient(&http.Client{Timeout: time.Second}, cfg, nil)

	err := client.Do(context.Background(), Request{Method: http.MethodPost, Endpoint: "/chat/completions"}, nil)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
	require.Equal(t, "mockB", gatewayErr.Provider)
	require.NotContains(t, gatewayErr.Message, baseURL)
	require.NotContains(t, gatewayErr.Message, "127.0.0.1", "Message = %q, must not disclose the upstream URL", gatewayErr.Message)

	// The full error stays available server-side for logs and diagnostics.
	require.Error(t, gatewayErr.Err)
	require.Contains(t, gatewayErr.Err.Error(), baseURL)
}

func mustAddr(t *testing.T, addr string) net.Addr {
	t.Helper()
	parsed, err := net.ResolveTCPAddr("tcp", addr)
	require.NoError(t, err, "ResolveTCPAddr(%q) = %v", addr, err)

	return parsed
}
