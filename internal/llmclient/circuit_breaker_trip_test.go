package llmclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	goconfig "github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const quotaErrorBody = `{"error":{"message":"quota exceeded for organization, window refreshes soon","code":"quota_exceeded"}}`

func newQuotaTripTestClient(t *testing.T, serverURL string, mutate func(cb *goconfig.CircuitBreakerConfig)) *Client {
	t.Helper()

	cfg := DefaultConfig("test", serverURL)
	cfg.Retry.MaxRetries = 0
	cfg.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 5,
		SuccessThreshold: 1,
		Timeout:          20 * time.Millisecond,
		TripOn: []goconfig.TripRuleConfig{
			{Match: `quota (exceeded|exhausted)`, TTL: 150 * time.Millisecond},
		},
	}
	if mutate != nil {
		mutate(&cfg.CircuitBreaker)
	}
	return New(cfg, nil)
}

// A matching upstream error message opens the breaker on the first failure,
// and the next request is rejected locally without reaching the upstream.
func TestTripRule_MatchingMessageTripsInstantly(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(quotaErrorBody))
	}))
	defer server.Close()

	// 403 is not a failure status by default, so only the message match can
	// explain the trip.
	client := newQuotaTripTestClient(t, server.URL, nil)

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusForbidden, gatewayErr.StatusCode)
	assert.Equal(t, int32(1), attempts.Load())
	assert.Equal(t, "open", client.circuitBreaker.State())

	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	require.ErrorAs(t, err, &gatewayErr)
	assert.Contains(t, gatewayErr.Message, "circuit breaker is open")
	assert.Equal(t, int32(1), attempts.Load(), "rejected request must not reach the upstream")
}

// A matching quota error on a retryable status trips on the first attempt:
// the retry loop stops instead of hammering the quota-exhausted provider
// max_retries+1 times.
func TestTripRule_RetryableQuotaErrorTripsBeforeRetry(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(quotaErrorBody))
	}))
	defer server.Close()

	cfg := DefaultConfig("test", server.URL)
	cfg.Retry.MaxRetries = 2
	cfg.CircuitBreaker = goconfig.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 5,
		SuccessThreshold: 1,
		Timeout:          20 * time.Millisecond,
		TripOn:           []goconfig.TripRuleConfig{{Match: `quota exceeded`, TTL: 150 * time.Millisecond}},
	}
	client := New(cfg, nil)

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusTooManyRequests, gatewayErr.StatusCode)
	assert.Equal(t, int32(1), attempts.Load(), "quota trip must stop the retry loop after the first attempt")
	assert.Equal(t, "open", client.circuitBreaker.State())
}

// The quota window outlives the breaker timeout: while quotaUntil is in the
// future the breaker stays open even after lastFailure ages past timeout.
// Once the window lapses the half-open probe decides recovery.
func TestTripRule_TTLExpiryAndHalfOpenProbe(t *testing.T) {
	t.Parallel()

	t.Run("probe success closes", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(quotaErrorBody))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		}))
		defer server.Close()

		client := newQuotaTripTestClient(t, server.URL, nil)

		err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		assert.Equal(t, "open", client.circuitBreaker.State())

		// Past the breaker timeout (20ms) but inside the quota window.
		time.Sleep(45 * time.Millisecond)
		err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Contains(t, gatewayErr.Message, "circuit breaker is open")
		assert.Equal(t, int32(1), attempts.Load(), "quota window must keep the breaker closed to traffic")

		// Past the quota window: the probe goes upstream and closes the breaker.
		time.Sleep(150 * time.Millisecond)
		err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.NoError(t, err)
		assert.Equal(t, int32(2), attempts.Load())
		assert.Equal(t, "closed", client.circuitBreaker.State())
	})

	t.Run("probe failure reopens", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(quotaErrorBody))
		}))
		defer server.Close()

		client := newQuotaTripTestClient(t, server.URL, nil)

		err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		time.Sleep(200 * time.Millisecond)

		// The probe still sees the quota error: the trip window restarts.
		err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr)
		assert.Equal(t, http.StatusForbidden, gatewayErr.StatusCode)
		assert.Equal(t, "open", client.circuitBreaker.State())

		err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		require.ErrorAs(t, err, &gatewayErr)
		assert.Contains(t, gatewayErr.Message, "circuit breaker is open")
		assert.Equal(t, int32(2), attempts.Load())
	})
}

// A zero rule TTL defers to the breaker's open-state timeout at trip time.
func TestTripRule_ZeroTTLUsesBreakerTimeout(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(quotaErrorBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, func(cb *goconfig.CircuitBreakerConfig) {
		cb.Timeout = 50 * time.Millisecond
		cb.TripOn = []goconfig.TripRuleConfig{{Match: `quota exceeded`}}
	})

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Equal(t, "open", client.circuitBreaker.State())

	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load())

	time.Sleep(80 * time.Millisecond)
	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load())
	assert.Equal(t, "closed", client.circuitBreaker.State())
}

// Without a message match nothing changes: failures still accumulate toward
// the configured threshold instead of tripping instantly.
func TestTripRule_NonMatchingFailureKeepsThresholdSemantics(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal server error"}}`))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, func(cb *goconfig.CircuitBreakerConfig) {
		cb.FailureThreshold = 2
	})

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Equal(t, "closed", client.circuitBreaker.State(), "non-matching failure must not trip instantly")

	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Equal(t, "open", client.circuitBreaker.State(), "threshold must still open the breaker")
	assert.Equal(t, int32(2), attempts.Load())

	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Contains(t, gatewayErr.Message, "circuit breaker is open")
	assert.Equal(t, int32(2), attempts.Load())
}

// No rules configured: the feature is inert and even a quota-shaped error
// leaves the breaker closed.
func TestTripRule_NoRulesStaysInert(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(quotaErrorBody))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, func(cb *goconfig.CircuitBreakerConfig) {
		cb.TripOn = nil
	})
	require.Empty(t, client.tripRules)

	for i := range 2 {
		err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
		require.Error(t, err)
		var gatewayErr *core.GatewayError
		require.ErrorAs(t, err, &gatewayErr, "attempt %d", i+1)
		assert.Equal(t, http.StatusForbidden, gatewayErr.StatusCode, "attempt %d", i+1)
	}
	assert.Equal(t, int32(2), attempts.Load())
	assert.Equal(t, "closed", client.circuitBreaker.State())
}

// ResetBreaker force-closes the provider-level breaker and every model-scoped
// breaker, ending a quota trip immediately.
func TestResetBreaker_ClearsProviderAndModelBreakers(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(quotaErrorBody))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, func(cb *goconfig.CircuitBreakerConfig) {
		cb.Scope = "model"
		cb.Timeout = time.Minute
		cb.TripOn = []goconfig.TripRuleConfig{{Match: `quota exceeded`}}
	})

	req := Request{Method: http.MethodGet, Endpoint: "/test", Model: "m1"}
	err := client.Do(context.Background(), req, nil)
	require.Error(t, err)

	var modelBreaker *circuitBreaker
	for _, entry := range client.modelBreakers {
		modelBreaker = entry.breaker
	}
	require.NotNil(t, modelBreaker, "model-scoped breaker must exist after a request")
	assert.Equal(t, "open", modelBreaker.State(), "the model breaker carries the trip")

	// The provider-level breaker serves empty/unknown models; trip it too so
	// both maps are exercised.
	client.circuitBreaker.RecordQuotaTrip(time.Minute)
	require.Equal(t, "open", client.circuitBreaker.State())

	client.ResetBreaker()
	assert.Equal(t, "closed", client.circuitBreaker.State())
	assert.Equal(t, "closed", modelBreaker.State())

	err = client.Do(context.Background(), req, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load())
}

// An invalid rule pattern surfaces through the same configErr channel as the
// other resilience validation errors.
func TestTripRule_InvalidRegexReportsConfigError(t *testing.T) {
	t.Parallel()

	client := New(Config{
		ProviderName: "test",
		BaseURL:      "http://localhost",
		CircuitBreaker: goconfig.CircuitBreakerConfig{
			Enabled: true,
			TripOn:  []goconfig.TripRuleConfig{{Match: "(unclosed"}},
		},
	}, nil)
	require.Error(t, client.configErr)
	assert.Contains(t, client.configErr.Error(), "trip_on")

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid resilience configuration")
}

func TestQuotaTripTTL(t *testing.T) {
	t.Parallel()

	timeout := time.Minute
	quotaErr := core.NewProviderError("test", http.StatusForbidden, "quota exceeded", nil)
	codedErr := core.NewProviderError("test", http.StatusForbidden, "usage limit reached", nil).WithCode("insufficient_quota")

	tests := []struct {
		name    string
		tripOn  []goconfig.TripRuleConfig
		err     error
		wantTTL time.Duration
		wantOK  bool
	}{
		{
			name:   "no rules is inert",
			tripOn: nil,
			err:    quotaErr,
		},
		{
			name:   "nil error never matches",
			tripOn: []goconfig.TripRuleConfig{{Match: "quota"}},
			err:    nil,
		},
		{
			name:   "non-gateway error never matches",
			tripOn: []goconfig.TripRuleConfig{{Match: "quota"}},
			err:    errors.New("quota exceeded"),
		},
		{
			name:    "matching message uses rule TTL",
			tripOn:  []goconfig.TripRuleConfig{{Match: `quota exceeded`, TTL: 5 * time.Minute}},
			err:     quotaErr,
			wantTTL: 5 * time.Minute,
			wantOK:  true,
		},
		{
			name:    "matching code alone matches",
			tripOn:  []goconfig.TripRuleConfig{{Match: `insufficient_quota$`}},
			err:     codedErr,
			wantTTL: timeout,
			wantOK:  true,
		},
		{
			name:    "zero rule TTL substitutes breaker timeout",
			tripOn:  []goconfig.TripRuleConfig{{Match: `quota exceeded`}},
			err:     quotaErr,
			wantTTL: timeout,
			wantOK:  true,
		},
		{
			name: "first matching rule wins",
			tripOn: []goconfig.TripRuleConfig{
				{Match: `no match here`, TTL: time.Second},
				{Match: `quota exceeded`, TTL: 2 * time.Minute},
			},
			err:     quotaErr,
			wantTTL: 2 * time.Minute,
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := New(Config{
				ProviderName: "test",
				BaseURL:      "http://localhost",
				CircuitBreaker: goconfig.CircuitBreakerConfig{
					Enabled: true,
					Timeout: timeout,
					TripOn:  tt.tripOn,
				},
			}, nil)
			require.NoError(t, client.configErr)

			ttl, ok := client.quotaTripTTL(tt.err)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantTTL, ttl)
		})
	}
}

// Breaker-level behavior: the quota window and the open-state timeout gate
// the half-open transition independently.
func TestCircuitBreaker_RecordQuotaTrip(t *testing.T) {
	t.Parallel()

	t.Run("window outlives timeout", func(t *testing.T) {
		t.Parallel()

		cb := newCircuitBreaker(3, 1, 20*time.Millisecond)
		cb.RecordQuotaTrip(120 * time.Millisecond)
		assert.Equal(t, "open", cb.State())

		allowed, probe := cb.acquire()
		assert.False(t, allowed)
		assert.False(t, probe)

		// Past the timeout but inside the quota window: still open.
		time.Sleep(45 * time.Millisecond)
		allowed, probe = cb.acquire()
		assert.False(t, allowed)
		assert.False(t, probe)

		// Past the quota window: the half-open probe is granted.
		time.Sleep(120 * time.Millisecond)
		allowed, probe = cb.acquire()
		assert.True(t, allowed)
		assert.True(t, probe)
	})

	t.Run("zero ttl keeps zero window", func(t *testing.T) {
		t.Parallel()

		cb := newCircuitBreaker(3, 1, 20*time.Millisecond)
		cb.RecordQuotaTrip(0)

		allowed, probe := cb.acquire()
		assert.False(t, allowed)
		assert.False(t, probe)

		time.Sleep(30 * time.Millisecond)
		allowed, probe = cb.acquire()
		assert.True(t, allowed)
		assert.True(t, probe)
	})

	t.Run("zero quotaUntil does not change plain timeout behavior", func(t *testing.T) {
		t.Parallel()

		cb := newCircuitBreaker(1, 1, 20*time.Millisecond)
		cb.RecordFailure()
		assert.Equal(t, "open", cb.State())

		allowed, probe := cb.acquire()
		assert.False(t, allowed)
		assert.False(t, probe)

		time.Sleep(30 * time.Millisecond)
		allowed, probe = cb.acquire()
		assert.True(t, allowed)
		assert.True(t, probe)
	})
}

func TestCircuitBreaker_Reset(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(2, 1, time.Hour)
	cb.RecordFailure()
	cb.RecordQuotaTrip(time.Hour)
	require.Equal(t, "open", cb.State())

	allowed, _ := cb.acquire()
	assert.False(t, allowed)

	cb.Reset()
	assert.Equal(t, "closed", cb.State())

	allowed, probe := cb.acquire()
	assert.True(t, allowed)
	assert.False(t, probe, "a reset breaker admits traffic without consuming a probe slot")

	// The failure count restarts from zero: the next failure does not open.
	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())
}

// A matching quota error in a streaming response body opens the breaker on
// the first stream establishment, just like the non-streaming Do path. When
// the provider returns a non-200 status the error body is parsed identically
// to the response path and quotaTripTTL fires.
func TestTripRule_DoStreamMatchingErrorTripsInstantly(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(quotaErrorBody))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, nil)

	stream, err := client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/chat"})
	require.Error(t, err)
	require.Nil(t, stream)
	assert.Equal(t, int32(1), attempts.Load())
	assert.Equal(t, "open", client.circuitBreaker.State())

	// The second DoStream call must be rejected locally.
	stream, err = client.DoStream(context.Background(), Request{Method: http.MethodPost, Endpoint: "/chat"})
	require.Error(t, err)
	require.Nil(t, stream)
	assert.Equal(t, int32(1), attempts.Load(), "rejected stream must not reach the upstream")
}

// Some providers answer 200 with a bare {"error": ...} body. A quota message
// there must trip the breaker exactly like a translated error status.
func TestTripRule_Embedded200QuotaErrorTripsInstantly(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(quotaErrorBody))
	}))
	defer server.Close()

	client := newQuotaTripTestClient(t, server.URL, nil)

	err := client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.ErrorIs(t, err, core.ErrEmbeddedInSuccess)
	assert.Equal(t, "open", client.circuitBreaker.State())

	err = client.Do(context.Background(), Request{Method: http.MethodGet, Endpoint: "/test"}, nil)
	require.Error(t, err)
	require.ErrorAs(t, err, &gatewayErr)
	assert.Contains(t, gatewayErr.Message, "circuit breaker is open")
	assert.Equal(t, int32(1), attempts.Load(), "rejected request must not reach the upstream")
}
