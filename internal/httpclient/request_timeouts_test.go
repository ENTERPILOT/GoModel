package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestTimeoutOverrides(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(100 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(upstream.Close)
	zero, short, long := time.Duration(0), 20*time.Millisecond, time.Second
	for _, tc := range []struct {
		name          string
		total, header time.Duration
		overrides     RequestTimeouts
		wantError     bool
	}{
		{"inherit total", short, long, RequestTimeouts{}, true},
		{"inherit headers", long, short, RequestTimeouts{}, true},
		{"disable both", short, short, RequestTimeouts{&zero, &zero}, false},
		{"extend both", short, short, RequestTimeouts{&long, &long}, false},
		{"short total", long, long, RequestTimeouts{Timeout: &short}, true},
		{"short headers", long, long, RequestTimeouts{ResponseHeaderTimeout: &short}, true},
		{"header still inherited", short, short, RequestTimeouts{Timeout: &zero}, true},
		{"total still inherited", short, short, RequestTimeouts{ResponseHeaderTimeout: &zero}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.Timeout, cfg.ResponseHeaderTimeout = tc.total, tc.header
			client := NewHTTPClient(&cfg)
			defer client.CloseIdleConnections()
			ctx := WithRequestTimeouts(t.Context(), tc.overrides)
			// Reuse the context for multiple attempts: no cumulative deadline.
			for range 2 {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				require.NoError(t, err)
				resp, err := Do(client, req)
				if tc.wantError {
					require.Error(t, err)
					continue
				}
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, "ok", string(body))
			}
			require.Equal(t, tc.total, client.Timeout)
			require.Equal(t, tc.header, underlyingTransport(t, client).ResponseHeaderTimeout)
		})
	}
}

func TestRequestTimeoutIncludesResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(150 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	client := NewDefaultHTTPClient()
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		wantError bool
	}{
		{"body times out", 30 * time.Millisecond, true},
		{"disabled body timeout", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithRequestTimeouts(t.Context(), RequestTimeouts{Timeout: &tc.timeout})
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			require.NoError(t, err)
			resp, err := Do(client, req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "ok", string(body))
		})
	}
}

func TestConcurrentTimeoutOverridesAreIsolated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	cfg := DefaultConfig()
	cfg.Timeout, cfg.ResponseHeaderTimeout = 30*time.Millisecond, 30*time.Millisecond
	client := NewHTTPClient(&cfg)
	defer client.CloseIdleConnections()
	zero := time.Duration(0)
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(WithRequestTimeouts(t.Context(), RequestTimeouts{&zero, &zero}), http.MethodGet, upstream.URL, nil)
		if err == nil {
			var resp *http.Response
			resp, err = Do(client, req)
			if err == nil {
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
		}
		done <- err
	}()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	_, err = Do(client, req)
	require.Error(t, err, "ordinary request must retain the global timeout")
	require.NoError(t, <-done)
}

func TestDisabledRequestTimeoutPreservesCancellation(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(upstream.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	zero := time.Duration(0)
	ctx = WithRequestTimeouts(ctx, RequestTimeouts{&zero, &zero})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	done := make(chan error, 1)
	client := NewDefaultHTTPClient()
	defer client.CloseIdleConnections()
	go func() { _, err := Do(client, req); done <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Error("upstream was not cancelled")
	}
}

func TestRequestTimeoutOverridesFollowTLSReload(t *testing.T) {
	resetTLS(t)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	client := NewDefaultHTTPClient()
	defer client.CloseIdleConnections()
	zero := time.Duration(0)
	ctx := WithRequestTimeouts(t.Context(), RequestTimeouts{&zero, &zero})
	request := func(wantError bool) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
		require.NoError(t, err)
		resp, err := Do(client, req)
		if resp != nil {
			require.NoError(t, resp.Body.Close())
		}
		if wantError {
			require.ErrorContains(t, err, "certificate")
			return
		}
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
	request(true)
	before := SnapshotTLS()
	require.NoError(t, SetConfiguredTLS(TLSSettings{InsecureSkipVerify: true}))
	request(false)
	RestoreTLS(before)
	request(true)
}

func TestResponseHeaderTimeoutOverridePreservesWebSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		kind, data, err := conn.Read(r.Context())
		if assert.NoError(t, err) {
			assert.NoError(t, conn.Write(r.Context(), kind, data))
		}
	}))
	defer upstream.Close()
	client := NewDefaultHTTPClient()
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	zero := time.Duration(0)
	ctx = WithRequestTimeouts(ctx, RequestTimeouts{ResponseHeaderTimeout: &zero})
	conn, _, err := websocket.Dial(ctx, upstream.URL, &websocket.DialOptions{HTTPClient: client})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("ping")))
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "ping", string(data))
}
