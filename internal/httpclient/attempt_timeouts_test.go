package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowHeadersServer waits delay before sending response headers and counts
// the TCP connections it accepts.
func slowHeadersServer(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delayHeaders(r, delay)
		w.WriteHeader(http.StatusOK)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, &conns
}

// delayHeaders holds the response for d, or until the client gives up.
func delayHeaders(r *http.Request, d time.Duration) {
	select {
	case <-time.After(d):
	case <-r.Context().Done():
	}
}

func sendWithTimeouts(t *testing.T, client *http.Client, url string, timeouts AttemptTimeouts) error {
	t.Helper()
	ctx := WithAttemptTimeouts(context.Background(), timeouts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := Do(client, req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func testClient(timeout, headerTimeout time.Duration) *http.Client {
	cfg := DefaultConfig()
	cfg.Timeout = timeout
	cfg.ResponseHeaderTimeout = headerTimeout
	return NewHTTPClient(&cfg)
}

func TestDoAppliesTimeoutOverride(t *testing.T) {
	server, _ := slowHeadersServer(t, 150*time.Millisecond)

	tests := []struct {
		name     string
		client   time.Duration
		override time.Duration
		wantErr  bool
	}{
		{name: "inherits client timeout", client: 50 * time.Millisecond, wantErr: true},
		{name: "longer override succeeds", client: 50 * time.Millisecond, override: 2 * time.Second},
		{name: "shorter override fails", client: 2 * time.Second, override: 50 * time.Millisecond, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(tt.client, 0)
			err := sendWithTimeouts(t, client, server.URL, AttemptTimeouts{Timeout: tt.override})
			if tt.wantErr {
				// The message depends on where the deadline fires (dial,
				// headers or body), so check the error kind, not its text.
				var netErr net.Error
				require.ErrorAs(t, err, &netErr)
				assert.True(t, netErr.Timeout(), "want a timeout, got %v", err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.client, client.Timeout, "the shared client is never mutated")
		})
	}
}

func TestLongerTimeoutExtendsHeaderWait(t *testing.T) {
	server, _ := slowHeadersServer(t, 150*time.Millisecond)

	tests := []struct {
		name     string
		override AttemptTimeouts
		wantErr  bool
	}{
		{name: "longer timeout extends the header wait", override: AttemptTimeouts{Timeout: 2 * time.Second}},
		{
			name:     "an explicit header timeout still applies",
			override: AttemptTimeouts{Timeout: 2 * time.Second, ResponseHeaderTimeout: 50 * time.Millisecond},
			wantErr:  true,
		},
		{name: "a shorter timeout keeps the header wait", override: AttemptTimeouts{Timeout: 40 * time.Millisecond}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(50*time.Millisecond, 50*time.Millisecond)
			err := sendWithTimeouts(t, client, server.URL, tt.override)
			if tt.wantErr {
				var netErr net.Error
				require.ErrorAs(t, err, &netErr)
				assert.True(t, netErr.Timeout(), "want a timeout, got %v", err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResponseHeaderTimeoutOverride(t *testing.T) {
	server, _ := slowHeadersServer(t, 150*time.Millisecond)

	tests := []struct {
		name     string
		base     time.Duration
		override time.Duration
		wantErr  bool
	}{
		{name: "inherits transport timeout", base: 50 * time.Millisecond, wantErr: true},
		{name: "longer override succeeds", base: 50 * time.Millisecond, override: 2 * time.Second},
		{name: "shorter override fails", base: 2 * time.Second, override: 50 * time.Millisecond, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(0, tt.base)
			err := sendWithTimeouts(t, client, server.URL, AttemptTimeouts{ResponseHeaderTimeout: tt.override})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "timeout awaiting response headers")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestResponseHeaderTimeoutOverrideReusesConnections(t *testing.T) {
	server, conns := slowHeadersServer(t, 0)
	client := testClient(0, time.Second)
	override := AttemptTimeouts{ResponseHeaderTimeout: 5 * time.Second}

	for range 3 {
		require.NoError(t, sendWithTimeouts(t, client, server.URL, override))
	}
	assert.Equal(t, int32(1), conns.Load(), "overridden attempts share one pooled connection")
}

func TestHeaderTimeoutVariantsAreCapped(t *testing.T) {
	var variants headerTimeoutVariants
	built := 0
	build := func() *http.Transport {
		built++
		return &http.Transport{}
	}
	first := variants.get(time.Second, build)
	assert.Same(t, first, variants.get(time.Second, build))
	for i := range maxHeaderTimeoutVariants {
		variants.get(time.Duration(i+2)*time.Second, build)
	}
	assert.Len(t, variants.transports, maxHeaderTimeoutVariants, "past the cap one pool is evicted per new value")
	assert.Equal(t, maxHeaderTimeoutVariants+1, built)
}

func TestWithAttemptTimeoutsZeroKeepsContext(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ctx, WithAttemptTimeouts(ctx, AttemptTimeouts{}))

	overridden := WithAttemptTimeouts(ctx, AttemptTimeouts{Timeout: time.Second})
	cleared := WithAttemptTimeouts(overridden, AttemptTimeouts{})
	assert.True(t, AttemptTimeoutsFromContext(cleared).IsZero(), "a zero value clears inherited overrides")
}

func TestShortensTimeouts(t *testing.T) {
	client := testClient(10*time.Second, 5*time.Second)

	tests := []struct {
		name     string
		timeouts AttemptTimeouts
		want     bool
	}{
		{name: "no override"},
		{name: "longer timeout", timeouts: AttemptTimeouts{Timeout: time.Minute}},
		{name: "same timeout", timeouts: AttemptTimeouts{Timeout: 10 * time.Second}},
		{name: "shorter timeout", timeouts: AttemptTimeouts{Timeout: time.Second}, want: true},
		{name: "longer header timeout", timeouts: AttemptTimeouts{ResponseHeaderTimeout: time.Minute}},
		{name: "shorter header timeout", timeouts: AttemptTimeouts{ResponseHeaderTimeout: time.Second}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShortensTimeouts(client, tt.timeouts))
		})
	}

	assert.True(t, ShortensTimeouts(testClient(0, 0), AttemptTimeouts{Timeout: time.Second}), "any limit is shorter than none")
	assert.False(t, ShortensTimeouts(nil, AttemptTimeouts{Timeout: time.Second}))
}
