package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
)

func testEvent() ext.AlertEvent {
	projected := 130.0
	return ext.AlertEvent{
		Type:      ext.AlertTypeBudgetThreshold,
		ID:        "bgt_test",
		Timestamp: time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC),
		Budget: ext.AlertBudget{
			Scope: "user_path", Subject: "/team", EffectiveSubject: "/team",
			PeriodSeconds: 86400, Period: "daily", Amount: 100, CostBasis: "provider",
		},
		ThresholdPercent: 80,
		Spent:            85,
		UsageRatio:       0.85,
		PeriodStart:      time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC),
		PeriodEnd:        time.Date(2026, time.April, 26, 0, 0, 0, 0, time.UTC),
		ProjectedSpend:   &projected,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func statusResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}
}

func TestWebhookSignsTheBody(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{name: "signed", secret: "s3cret"},
		{name: "unsigned without a secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var header http.Header
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Clone()
				body, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			hook, err := newWebhook(WebhookConfig{URL: server.URL + "/hooks/token", Secret: tt.secret}, time.Second, 0)
			require.NoError(t, err)
			require.NoError(t, hook.send(context.Background(), testEvent()))

			assert.Equal(t, "application/json", header.Get("Content-Type"))
			assert.Equal(t, ext.AlertTypeBudgetThreshold, header.Get(HeaderEvent))
			assert.Equal(t, "bgt_test", header.Get(HeaderDelivery))
			assert.True(t, strings.HasPrefix(header.Get("User-Agent"), "GoModel/"))

			var got ext.AlertEvent
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, testEvent(), got)

			if tt.secret == "" {
				assert.Empty(t, header.Get(HeaderSignature))
				return
			}
			mac := hmac.New(sha256.New, []byte(tt.secret))
			mac.Write(body)
			assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), header.Get(HeaderSignature))
		})
	}
}

func TestWebhookRetriesWithBackoff(t *testing.T) {
	transient := errors.New("connection refused")
	tests := []struct {
		name       string
		maxRetries int
		// answers are the HTTP statuses of successive attempts; 0 is a
		// transport error. The last one repeats.
		answers      []int
		wantAttempts int
		wantWaits    []time.Duration
		wantErr      string
	}{
		{name: "first attempt succeeds", maxRetries: 3, answers: []int{200}, wantAttempts: 1},
		{name: "recovers after server errors", maxRetries: 3, answers: []int{500, 503, 204}, wantAttempts: 3,
			wantWaits: []time.Duration{time.Second, 2 * time.Second}},
		{name: "recovers after a transport error", maxRetries: 3, answers: []int{0, 200}, wantAttempts: 2,
			wantWaits: []time.Duration{time.Second}},
		{name: "retries rate limiting", maxRetries: 1, answers: []int{429, 200}, wantAttempts: 2,
			wantWaits: []time.Duration{time.Second}},
		{name: "gives up after max retries", maxRetries: 3, answers: []int{500}, wantAttempts: 4,
			wantWaits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, wantErr: "after 4 attempt(s): webhook answered HTTP 500"},
		{name: "does not retry a client error", maxRetries: 3, answers: []int{400}, wantAttempts: 1,
			wantErr: "after 1 attempt(s): webhook answered HTTP 400"},
		{name: "does not follow a redirect", maxRetries: 3, answers: []int{302}, wantAttempts: 1,
			wantErr: "webhook answered HTTP 302"},
		{name: "retries disabled", maxRetries: 0, answers: []int{500}, wantAttempts: 1,
			wantErr: "after 1 attempt(s): webhook answered HTTP 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hook, err := newWebhook(WebhookConfig{URL: "https://hooks.example.com/t0ken"}, time.Second, tt.maxRetries)
				require.NoError(t, err)

				var attempts []time.Time
				hook.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts = append(attempts, time.Now())
					status := tt.answers[min(len(attempts), len(tt.answers))-1]
					if status == 0 {
						return nil, transient
					}
					return statusResponse(status), nil
				})}

				err = hook.send(context.Background(), testEvent())
				if tt.wantErr == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tt.wantErr)
				}
				require.Len(t, attempts, tt.wantAttempts)
				var waits []time.Duration
				for i := 1; i < len(attempts); i++ {
					waits = append(waits, attempts[i].Sub(attempts[i-1]))
				}
				assert.Equal(t, tt.wantWaits, waits)
			})
		})
	}
}

func TestWebhookStopsRetryingWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hook, err := newWebhook(WebhookConfig{URL: "https://hooks.example.com/t0ken"}, time.Second, 5)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		hook.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			cancel() // shutdown arrives while the retry is pending
			return statusResponse(http.StatusBadGateway), nil
		})}

		err = hook.send(ctx, testEvent())
		require.Error(t, err)
		assert.Equal(t, 1, attempts)
	})
}

// A webhook URL commonly carries a token, and net/http puts the URL into its
// errors.
func TestWebhookErrorsNeverCarryTheURL(t *testing.T) {
	hook, err := newWebhook(WebhookConfig{URL: "https://user:pa55@hooks.example.com/services/t0ken?key=abc"}, time.Second, 0)
	require.NoError(t, err)
	hook.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})}

	err = hook.send(context.Background(), testEvent())
	require.ErrorContains(t, err, "connection refused")
	for _, secret := range []string{"t0ken", "pa55", "key=abc", "/services"} {
		assert.NotContains(t, err.Error(), secret)
	}
	assert.Equal(t, "hooks.example.com", hook.host)
}

func TestNewWebhookRejectsInvalidURLWithoutEchoingIt(t *testing.T) {
	for _, raw := range []string{"", "hooks.example.com/t0ken", "ftp://hooks.example.com/t0ken", "https:///t0ken", "${env:T0KEN_URL}", "://t0ken"} {
		t.Run(raw, func(t *testing.T) {
			_, err := newWebhook(WebhookConfig{URL: raw}, time.Second, 0)
			require.Error(t, err)
			assert.NotContains(t, strings.ToLower(err.Error()), "t0ken")
		})
	}
}
