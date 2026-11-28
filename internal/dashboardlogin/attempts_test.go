package dashboardlogin

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admitWait admits one attempt and returns only how long the client must wait.
func admitWait(limiter *attemptLimiter, clientIP string, now time.Time) time.Duration {
	_, wait := limiter.admit(clientIP, now)
	return wait
}

func TestAttemptLimiterLocksOutAfterRepeatedAttempts(t *testing.T) {
	limiter := newAttemptLimiter()
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"

	for range maxFailedAttempts {
		require.Zero(t, admitWait(limiter, client, start.Add(time.Minute)), "an attempt within the limit is admitted")
	}

	assert.Equal(t, attemptWindow-time.Minute, admitWait(limiter, client, start.Add(2*time.Minute)),
		"the lockout lasts until the window that began with the first attempt ends")
	assert.Zero(t, admitWait(limiter, "203.0.113.8", start.Add(2*time.Minute)), "another client is unaffected")
	assert.Zero(t, admitWait(limiter, client, start.Add(time.Minute+attemptWindow)), "the lockout ends with the window")
}

func TestAttemptLimiterResetForgetsAttempts(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"

	for range maxFailedAttempts {
		admitWait(limiter, client, now)
	}
	require.NotZero(t, admitWait(limiter, client, now))

	limiter.reset(client)
	assert.Zero(t, admitWait(limiter, client, now))
}

func TestAttemptLimiterReleaseGivesBackOneAttempt(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"

	limiter.release(client, nil) // a refused sign-in has nothing to give back
	var admission *attemptRecord
	for range maxFailedAttempts {
		admission, _ = limiter.admit(client, now)
	}
	require.NotNil(t, admission)
	require.NotZero(t, admitWait(limiter, client, now))

	limiter.release(client, admission)
	assert.Zero(t, admitWait(limiter, client, now), "the released attempt is available again")
	assert.NotZero(t, admitWait(limiter, client, now), "and only that one")
}

// A sign-in turned away after its record was replaced must not take an
// attempt off the record that replaced it.
func TestAttemptLimiterReleaseLeavesAReplacementRecordAlone(t *testing.T) {
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	const client = "203.0.113.7"
	tests := []struct {
		name string
		// replace swaps the client's record for a new one and returns the
		// time the new record's attempts are counted at.
		replace func(limiter *attemptLimiter) time.Time
	}{
		{name: "the window expired", replace: func(*attemptLimiter) time.Time { return start.Add(attemptWindow) }},
		{name: "a sign-in reset it", replace: func(limiter *attemptLimiter) time.Time {
			limiter.reset(client)
			return start
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := newAttemptLimiter()
			stale, _ := limiter.admit(client, start)
			require.NotNil(t, stale)

			now := tt.replace(limiter)
			for range maxFailedAttempts {
				require.Zero(t, admitWait(limiter, client, now))
			}
			require.NotZero(t, admitWait(limiter, client, now))

			limiter.release(client, stale)
			assert.NotZero(t, admitWait(limiter, client, now), "the new record keeps every attempt it counted")
		})
	}
}

func TestAttemptLimiterCountsAnIPv6SubnetAsOneClient(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	for i := range maxFailedAttempts {
		admitWait(limiter, fmt.Sprintf("2001:db8:1:2::%x", i+1), now)
	}

	assert.NotZero(t, admitWait(limiter, "2001:db8:1:2:ffff::1", now), "an address in the same /64 shares the count")
	assert.Zero(t, admitWait(limiter, "2001:db8:1:3::1", now), "another /64 does not")
}

func TestAttemptLimiterRefusesNewClientsWhenFull(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for i := range maxTrackedClients {
		admitWait(limiter, fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff), now)
	}

	assert.Equal(t, attemptWindow, admitWait(limiter, "203.0.113.7", now), "an untracked client waits for room")
	assert.Zero(t, admitWait(limiter, "10.0.0.1", now), "a tracked client under the limit may still try")
	assert.Zero(t, admitWait(limiter, "203.0.113.7", now.Add(attemptWindow)), "expired records make room")
	assert.Zero(t, admitWait(limiter, "10.0.0.2", now.Add(attemptWindow)), "a client whose record expired starts over")
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
