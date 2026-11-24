package dashboardlogin

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttemptLimiterLocksOutAfterRepeatedFailures(t *testing.T) {
	limiter := newAttemptLimiter()
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"

	for range maxFailedAttempts - 1 {
		limiter.fail(client, start)
	}
	require.Zero(t, limiter.retryAfter(client, start), "the last allowed attempt is still open")

	limiter.fail(client, start.Add(time.Minute))
	assert.Equal(t, attemptWindow-2*time.Minute, limiter.retryAfter(client, start.Add(2*time.Minute)),
		"the lockout lasts until the window that began with the first failure ends")
	assert.Zero(t, limiter.retryAfter("203.0.113.8", start), "another client is unaffected")
	assert.Zero(t, limiter.retryAfter(client, start.Add(attemptWindow)), "the lockout ends with the window")
}

func TestAttemptLimiterResetForgetsFailures(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"

	for range maxFailedAttempts {
		limiter.fail(client, now)
	}
	require.NotZero(t, limiter.retryAfter(client, now))

	limiter.reset(client)
	assert.Zero(t, limiter.retryAfter(client, now))
}

func TestAttemptLimiterCountsAnIPv6SubnetAsOneClient(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	for i := range maxFailedAttempts {
		limiter.fail(fmt.Sprintf("2001:db8:1:2::%x", i+1), now)
	}

	assert.NotZero(t, limiter.retryAfter("2001:db8:1:2:ffff::1", now), "an address in the same /64 shares the count")
	assert.Zero(t, limiter.retryAfter("2001:db8:1:3::1", now), "another /64 does not")
}

func TestAttemptLimiterRefusesNewClientsWhenFull(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for i := range maxTrackedClients {
		limiter.fail(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff), now)
	}

	assert.Equal(t, attemptWindow, limiter.retryAfter("203.0.113.7", now), "an untracked client waits for room")
	assert.Zero(t, limiter.retryAfter("10.0.0.1", now), "a tracked client under the limit may still try")
	assert.Zero(t, limiter.retryAfter("203.0.113.7", now.Add(attemptWindow)), "expired records make room")
}

func TestClientKey(t *testing.T) {
	tests := []struct {
		clientIP string
		want     string
	}{
		{clientIP: "203.0.113.7", want: "203.0.113.7"},
		{clientIP: "::ffff:203.0.113.7", want: "203.0.113.7"},
		{clientIP: "2001:db8:1:2:3:4:5:6", want: "2001:db8:1:2::/64"},
		{clientIP: "not-an-address", want: "not-an-address"},
		{clientIP: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.clientIP, func(t *testing.T) {
			assert.Equal(t, tt.want, clientKey(tt.clientIP))
		})
	}
}
