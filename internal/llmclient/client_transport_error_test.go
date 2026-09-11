package llmclient

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
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
			if got != tt.want {
				t.Fatalf("transportErrorMessage() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "secret-host.internal") || strings.Contains(got, "s3cret") {
				t.Fatalf("transportErrorMessage() = %q, must not disclose the upstream URL", got)
			}
		})
	}
}

func TestReadErrorMessage(t *testing.T) {
	if got := readErrorMessage(context.DeadlineExceeded); got != "timed out reading provider response" {
		t.Fatalf("readErrorMessage(timeout) = %q", got)
	}
	got := readErrorMessage(&net.OpError{Op: "read", Net: "tcp", Source: mustAddr(t, "127.0.0.1:51423"), Err: errors.New("connection reset by peer")})
	if got != "failed to read provider response" {
		t.Fatalf("readErrorMessage() = %q", got)
	}
	if strings.Contains(got, "127.0.0.1") {
		t.Fatalf("readErrorMessage() = %q, must not disclose connection details", got)
	}
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
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %v (%T), want *core.GatewayError", err, err)
	}
	if gatewayErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want %d", gatewayErr.StatusCode, http.StatusBadGateway)
	}
	if gatewayErr.Provider != "mockB" {
		t.Fatalf("Provider = %q, want %q", gatewayErr.Provider, "mockB")
	}
	if strings.Contains(gatewayErr.Message, baseURL) || strings.Contains(gatewayErr.Message, "127.0.0.1") {
		t.Fatalf("Message = %q, must not disclose the upstream URL", gatewayErr.Message)
	}
	// The full error stays available server-side for logs and diagnostics.
	if gatewayErr.Err == nil || !strings.Contains(gatewayErr.Err.Error(), baseURL) {
		t.Fatalf("wrapped error = %v, want the full transport error", gatewayErr.Err)
	}
}

func mustAddr(t *testing.T, addr string) net.Addr {
	t.Helper()
	parsed, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatalf("ResolveTCPAddr(%q) = %v", addr, err)
	}
	return parsed
}
