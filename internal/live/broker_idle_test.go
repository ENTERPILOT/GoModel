package live

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

// auditEntryWithBodies is an in-flight entry carrying request metadata and
// both bodies, so every audit preview variant has something to strip.
func auditEntryWithBodies(headerValue string) *auditlog.LogEntry {
	return &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
		StatusCode: 200,
		Data: &auditlog.LogData{
			RequestHeaders:  map[string]string{"Authorization": "[REDACTED]", "X-Trace": headerValue},
			RequestBody:     auditlog.CaptureLoggedBody([]byte(`{"model":"gpt-test"}`)),
			ResponseHeaders: map[string]string{"X-Request-ID": "req-1"},
			ResponseBody:    auditlog.CaptureLoggedBody([]byte(`{"id":"chatcmpl-test"}`)),
		},
	}
}

// Skipping the fan-out encoding while nobody listens must not change what the
// broker retains: a dashboard that connects later replays exactly the same
// events and active snapshots as one that was connected all along.
func TestBrokerRetainsSameEventsWithoutSubscribers(t *testing.T) {
	cases := []struct {
		name        string
		headerValue string
	}{
		{name: "bodies stripped", headerValue: "short"},
		{name: "bodies stripped and compacted", headerValue: strings.Repeat("x", maxRetainedEventBytes)},
	}
	events := []string{EventAuditStarted, EventAuditUpdated, EventAuditCompleted}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idle := NewBroker(Config{Enabled: true})
			watched := NewBroker(Config{Enabled: true})
			sub := watched.Subscribe(0)
			require.NotNil(t, sub)
			defer sub.Close()

			for _, eventType := range events {
				idle.PublishAuditEvent(eventType, auditEntryWithBodies(tc.headerValue))
				watched.PublishAuditEvent(eventType, auditEntryWithBodies(tc.headerValue))
				<-sub.Events
			}

			require.Len(t, idle.events, len(events))
			require.Len(t, watched.events, len(events))
			for i := range events {
				assert.JSONEq(t, string(watched.events[i].Data), string(idle.events[i].Data), "retained event %d", i)
			}
			fresh := idle.Subscribe(0)
			require.NotNil(t, fresh)
			defer fresh.Close()
			freshWatched := watched.Subscribe(0)
			require.NotNil(t, freshWatched)
			defer freshWatched.Close()
			require.Len(t, fresh.Replay, 1)
			require.Len(t, freshWatched.Replay, 1)
			assert.JSONEq(t, string(freshWatched.Replay[0].Data), string(fresh.Replay[0].Data), "active snapshot")
		})
	}
}

// The live copy still carries the bodies whenever someone is subscribed, and
// the retained copy never does.
func TestBrokerFansOutBodiesOnlyToSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)
	defer sub.Close()

	b.PublishAuditEvent(EventAuditCompleted, auditEntryWithBodies("short"))

	live := eventPayload(t, <-sub.Events)
	liveData, ok := live["data"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, liveData, "request_body")
	assert.Contains(t, liveData, "response_body")

	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, retainedData, "request_body")
	assert.NotContains(t, retainedData, "response_body")
	assert.Equal(t, true, retainedData["request_body_captured"])
	assert.Equal(t, true, retainedData["response_body_captured"])
}

// The lock-free subscriber count backs both HasLiveSubscribers and the
// fan-out decision, so every way a subscriber leaves has to update it.
func TestBrokerSubscriberCountTracksEveryExit(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		b := NewBroker(Config{Enabled: true})
		sub := b.Subscribe(0)
		require.True(t, b.HasLiveSubscribers())
		sub.Close()
		assert.False(t, b.HasLiveSubscribers())
	})

	t.Run("slow subscriber dropped", func(t *testing.T) {
		b := NewBroker(Config{Enabled: true, SubscriberBuffer: 1})
		sub := b.Subscribe(0)
		require.NotNil(t, sub)
		for range 3 {
			b.PublishAuditEvent(EventAuditUpdated, auditEntryWithBodies("short"))
		}
		assert.False(t, b.HasLiveSubscribers(), "a dropped subscriber must not keep bodies encoding")
	})

	t.Run("broker close", func(t *testing.T) {
		b := NewBroker(Config{Enabled: true})
		b.Subscribe(0)
		b.Subscribe(0)
		require.True(t, b.HasLiveSubscribers())
		b.Close()
		assert.False(t, b.HasLiveSubscribers())
	})
}
