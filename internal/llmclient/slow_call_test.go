package llmclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const slowCallDelay = 30 * time.Millisecond

// newSlowServer answers every request with a 200 after slowCallDelay.
func newSlowServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(slowCallDelay)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSlowCallsTripCircuitBreaker(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold time.Duration
		wantState string
	}{
		{"slower than threshold opens the circuit", slowCallDelay / 10, "open"},
		{"faster than threshold stays closed", time.Minute, "closed"},
		{"zero threshold disables the trigger", 0, "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newSlowServer(t, "application/json", `{"ok":true}`)
			cfg := DefaultConfig("test", server.URL)
			cfg.CircuitBreaker.FailureThreshold = 2
			cfg.CircuitBreaker.SlowCallThreshold = tc.threshold
			client := New(cfg, nil)

			for range cfg.CircuitBreaker.FailureThreshold {
				resp, err := client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
				require.NoError(t, err, "a slow answer is still returned to the caller")
				assert.JSONEq(t, `{"ok":true}`, string(resp.Body))
			}
			assert.Equal(t, tc.wantState, client.circuitBreaker.State())
		})
	}
}

func TestSlowStreamStartTripsCircuitBreaker(t *testing.T) {
	server := newSlowServer(t, "text/event-stream", "data: {}\n\ndata: [DONE]\n\n")
	cfg := DefaultConfig("test", server.URL)
	cfg.CircuitBreaker.FailureThreshold = 1
	cfg.CircuitBreaker.SlowCallThreshold = slowCallDelay / 10
	client := New(cfg, nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/test"})
	require.NoError(t, err)
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.Equal(t, "open", client.circuitBreaker.State())
}

func TestSlowHalfOpenProbeReopensCircuit(t *testing.T) {
	server := newSlowServer(t, "application/json", `{}`)
	cfg := DefaultConfig("test", server.URL)
	cfg.CircuitBreaker.SlowCallThreshold = slowCallDelay / 10
	client := New(cfg, nil)
	client.circuitBreaker.state = circuitOpen
	client.circuitBreaker.lastFailure = time.Now().Add(-cfg.CircuitBreaker.Timeout - time.Second)

	_, err := client.DoRaw(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.NoError(t, err)

	assert.Equal(t, "open", client.circuitBreaker.State())
}
