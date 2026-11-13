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
	"strconv"
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
			sentAt := time.Unix(1777118407, 0)
			hook.now = func() time.Time { return sentAt }
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
			// Recompute it the way the documentation tells a receiver to.
			mac := hmac.New(sha256.New, []byte(tt.secret))
			mac.Write([]byte("1777118407." + string(body)))
			assert.Equal(t, "t=1777118407,v1="+hex.EncodeToString(mac.Sum(nil)), header.Get(HeaderSignature))
			assert.True(t, verifySignature([]byte(tt.secret), header.Get(HeaderSignature), body, sentAt.Add(time.Minute)))
		})
	}
}

// verifySignature is the receiver side of the documented scheme: parse the
// pairs, reject a timestamp outside the tolerance, compare v1.
func verifySignature(secret []byte, header string, body []byte, now time.Time) bool {
	var timestamp, signature string
	for pair := range strings.SplitSeq(header, ",") {
		key, value, _ := strings.Cut(pair, "=")
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signature = value
		}
	}
	sent, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || now.Sub(time.Unix(sent, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

func TestSignatureVerification(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"id":"bgt_test"}`)
	sentAt := time.Unix(1777118407, 0)
	valid := Sign(secret, sentAt, body)
	tests := []struct {
		name   string
		header string
		body   []byte
		now    time.Time
		want   bool
	}{
		{name: "valid", header: valid, body: body, now: sentAt.Add(4 * time.Minute), want: true},
		{name: "an unknown future scheme is ignored", header: valid + ",v2=abc", body: body, now: sentAt, want: true},
		{name: "replayed past the tolerance", header: valid, body: body, now: sentAt.Add(6 * time.Minute)},
		{name: "changed body", header: valid, body: []byte(`{"id":"bgt_other"}`), now: sentAt},
		{name: "changed timestamp", header: strings.Replace(valid, "t=1777118407", "t=1777118408", 1), body: body, now: sentAt},
		{name: "wrong secret", header: Sign([]byte("other"), sentAt, body), body: body, now: sentAt},
		{name: "legacy body-only form", header: "sha256=" + strings.TrimPrefix(valid, "t=1777118407,v1="), body: body, now: sentAt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, verifySignature(secret, tt.header, tt.body, tt.now))
		})
	}
}

// Each attempt is signed at its own time, so a retry long after the first
// attempt still passes a receiver's tolerance window.
func TestWebhookSignsEveryAttemptAtItsTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hook, err := newWebhook(WebhookConfig{URL: "https://hooks.example.com/t0ken", Secret: "s3cret"}, time.Second, 2)
		require.NoError(t, err)
		var signatures []string
		hook.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			signatures = append(signatures, req.Header.Get(HeaderSignature))
			if len(signatures) < 3 {
				return statusResponse(http.StatusServiceUnavailable), nil
			}
			return statusResponse(http.StatusOK), nil
		})}
		start := time.Now()
		require.NoError(t, hook.send(context.Background(), testEvent()))
		require.Len(t, signatures, 3)
		assert.True(t, strings.HasPrefix(signatures[0], "t="+strconv.FormatInt(start.Unix(), 10)+",v1="))
		assert.True(t, strings.HasPrefix(signatures[2], "t="+strconv.FormatInt(start.Add(3*time.Second).Unix(), 10)+",v1="))
	})
}

// Against a real server: a client error is final, and only a same-origin
// 307 or 308 is followed, since those repeat the POST with its body.
func TestWebhookAgainstAServer(t *testing.T) {
	tests := []struct {
		name string
		// status answers /hook; a redirect points at location.
		status       int
		location     string
		wantErr      string
		wantHookHits int
		wantFinal    bool
	}{
		{name: "client error is not retried", status: http.StatusUnauthorized, wantErr: "after 1 attempt(s): webhook answered HTTP 401", wantHookHits: 1},
		{name: "same-origin 307 is followed with the body", status: http.StatusTemporaryRedirect, location: "/final", wantHookHits: 1, wantFinal: true},
		{name: "same-origin 308 is followed with the body", status: http.StatusPermanentRedirect, location: "/final", wantHookHits: 1, wantFinal: true},
		{name: "same-origin 302 is not followed", status: http.StatusFound, location: "/final", wantErr: "after 1 attempt(s): webhook answered HTTP 302", wantHookHits: 1},
		{name: "same-origin 303 is not followed", status: http.StatusSeeOther, location: "/final", wantErr: "webhook answered HTTP 303", wantHookHits: 1},
		{name: "cross-origin 307 is not followed", status: http.StatusTemporaryRedirect, location: "http://other.invalid/final", wantErr: "webhook answered HTTP 307", wantHookHits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookHits, finalHits := 0, 0
			var finalBody []byte
			var finalSignature string
			mux := http.NewServeMux()
			mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
				hookHits++
				_, _ = io.Copy(io.Discard, r.Body)
				if tt.location != "" {
					w.Header().Set("Location", tt.location)
				}
				w.WriteHeader(tt.status)
			})
			mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
				finalHits++
				assert.Equal(t, http.MethodPost, r.Method)
				finalBody, _ = io.ReadAll(r.Body)
				finalSignature = r.Header.Get(HeaderSignature)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			hook, err := newWebhook(WebhookConfig{URL: server.URL + "/hook", Secret: "s3cret"}, time.Second, 3)
			require.NoError(t, err)
			hook.backoff = time.Millisecond
			err = hook.send(context.Background(), testEvent())
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			assert.Equal(t, tt.wantHookHits, hookHits)
			if !tt.wantFinal {
				assert.Zero(t, finalHits)
				return
			}
			require.Equal(t, 1, finalHits)
			want, err := json.Marshal(testEvent())
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(finalBody))
			assert.True(t, verifySignature([]byte("s3cret"), finalSignature, finalBody, time.Now()))
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
