package live

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestBrokerPublishesAndReplaysBySequence(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 4, ReplayLimit: 4})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Method:    "POST",
		Path:      "/v1/chat/completions",
	})
	b.PublishUsageEvent(EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
		Model:     "gpt-test",
		Provider:  "openai",
	})

	sub := b.Subscribe(1)
	require.NotNil(t, sub)

	defer sub.Close()

	require.False(t, sub.Reset)
	require.Len(t, sub.Replay, 1)
	got := sub.Replay[0].Type
	require.Equal(t, EventUsageCompleted, got)

	require.EqualValues(t, 2, sub.Replay[0].Seq, "replay seq")
}

func TestSubscriptionLatestSnapshotsSequenceAtSubscribe(t *testing.T) {
	cases := []struct {
		name          string
		publishBefore int
		publishAfter  int
		want          uint64
	}{
		{name: "fresh broker", publishBefore: 0, publishAfter: 0, want: 0},
		{name: "existing events", publishBefore: 2, publishAfter: 0, want: 2},
		// Events published after subscribe arrive on the channel; Latest must
		// stay at the subscribe-time snapshot or a heartbeat built from it
		// would let a reconnecting client skip the queued events.
		{name: "later events stay off the snapshot", publishBefore: 1, publishAfter: 2, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBroker(Config{Enabled: true})
			publish := func(n int) {
				for range n {
					b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
						ID:        "audit-1",
						RequestID: "req-1",
						Timestamp: time.Now(),
					})
				}
			}
			publish(tc.publishBefore)
			sub := b.Subscribe(0)
			require.NotNil(t, sub)

			defer sub.Close()
			publish(tc.publishAfter)

			require.Equal(t, tc.want, sub.Latest)
		})
	}
}

func TestBrokerBroadcastsCachedUsageEvents(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishUsageEvent(EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-exact",
		RequestID: "req-exact",
		Timestamp: now,
		CacheType: " EXACT ",
	})
	b.PublishUsageEvent(EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-semantic",
		RequestID: "req-semantic",
		Timestamp: now.Add(time.Second),
		CacheType: usage.CacheTypeSemantic,
	})
	got := b.LatestSeq()
	require.Equal(t, uint64(2), got)

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Len(t, sub.Replay, 2)

	for i, event := range sub.Replay {
		require.Equal(t, EventUsageCompleted, event.Type, "replay[%d] type = %q, want %q", i, event.Type, EventUsageCompleted)
	}
}

func TestBrokerReplaysActiveSnapshotsForFreshSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Method:    "POST",
		Path:      "/v1/chat/completions",
	})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:             "audit-1",
		RequestID:      "req-1",
		Timestamp:      now.Add(time.Second),
		RequestedModel: "gpt-test",
		Provider:       "openai",
	})
	b.PublishUsageEvent(EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: now.Add(2 * time.Second),
		Model:     "gpt-test",
		Provider:  "openai",
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	require.False(t, sub.Reset)
	require.Len(t, sub.Replay, 2)
	got := sub.Replay[0].Type
	require.Equal(t, EventAuditUpdated, got)

	payload := eventPayload(t, sub.Replay[0])
	require.Equal(t, "POST", payload["method"], "snapshot method")
	require.Equal(t, "openai", payload["provider"], "snapshot provider")
	got = sub.Replay[1].Type
	require.Equal(t, EventUsageCompleted, got)
}

func TestBrokerNormalizesAuditActiveSnapshotAliases(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// The first event has no request ID, so its snapshot is keyed by entry ID;
	// the second carries both and must take over that snapshot rather than
	// start a second one.
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		Timestamp: now,
		Method:    "POST",
	})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
		Provider:  "openai",
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	require.Len(t, sub.Replay, 1)

	payload := eventPayload(t, sub.Replay[0])
	got := payload["method"]
	require.Equal(t, "POST", got)
	got = payload["provider"]
	require.Equal(t, "openai", got)

	b.PublishAuditEvent(EventAuditFlushed, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now.Add(2 * time.Second),
	})
	subAfterFlush := b.Subscribe(0)
	require.NotNil(t, subAfterFlush)

	defer subAfterFlush.Close()
	require.Empty(t, subAfterFlush.Replay)
}

func TestBrokerNormalizesUsageActiveSnapshotAliases(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.publish(EventUsageCompleted, "", "req-1", now, map[string]any{
		"request_id":   "req-1",
		"total_tokens": 14,
	})
	b.publish(EventUsageCompleted, "usage-1", "req-1", now.Add(time.Second), map[string]any{
		"id":         "usage-1",
		"request_id": "req-1",
		"model":      "gpt-test",
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	require.Len(t, sub.Replay, 1)

	payload := eventPayload(t, sub.Replay[0])
	got := payload["total_tokens"]
	require.Equal(t, float64(14), got)
	got = payload["model"]
	require.Equal(t, "gpt-test", got)

	b.publish(EventUsageFlushed, "usage-1", "req-1", now.Add(2*time.Second), map[string]any{
		"id":         "usage-1",
		"request_id": "req-1",
	})
	subAfterFlush := b.Subscribe(0)
	require.NotNil(t, subAfterFlush)

	defer subAfterFlush.Close()
	require.Empty(t, subAfterFlush.Replay)
}

func TestBrokerOmitsFlushedSnapshotsForFreshSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
	})
	b.PublishAuditEvent(EventAuditFlushed, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
	})
	b.PublishUsageEvent(EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: now,
	})
	b.PublishUsageEvent(EventUsageFlushed, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Empty(t, sub.Replay)
}

func TestBrokerStaleCursorReceivesResetAndActiveSnapshots(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
			ID:        "audit-1",
			RequestID: "req-1",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Method:    "POST",
		})
	}

	sub := b.Subscribe(1)
	require.NotNil(t, sub)

	defer sub.Close()
	require.True(t, sub.Reset)
	require.Len(t, sub.Replay, 1)
	got := sub.Replay[0].Seq
	require.Equal(t, uint64(3), got)
}

func TestBrokerSignalsResetWhenCursorFallsOutOfReplayWindow(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	for range 3 {
		b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
			ID:        "audit",
			RequestID: "req",
			Timestamp: time.Now(),
		})
	}

	sub := b.Subscribe(1)
	require.NotNil(t, sub)

	defer sub.Close()
	require.True(t, sub.Reset)
}

func TestBrokerSignalsResetWhenReplayGapExceedsLimit(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10, ReplayLimit: 2})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	for i := range 5 {
		b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
			ID:             "audit-1",
			RequestID:      "req-1",
			Timestamp:      now.Add(time.Duration(i) * time.Second),
			RequestedModel: "gpt-test",
			Provider:       "openai",
		})
	}

	sub := b.Subscribe(1)
	require.NotNil(t, sub)

	defer sub.Close()
	require.True(t, sub.Reset)
	require.Len(t, sub.Replay, 1)
	got := sub.Replay[0].Seq
	require.Equal(t, uint64(5), got)
}

func TestBrokerSignalsResetWhenCursorIsAheadOfLatest(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10, ReplayLimit: 10})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Method:    "POST",
	})

	sub := b.Subscribe(99)
	require.NotNil(t, sub)

	defer sub.Close()
	require.True(t, sub.Reset)
	require.Len(t, sub.Replay, 1)
	got := sub.Replay[0].Seq
	require.Equal(t, uint64(1), got)
}

func TestBrokerDropsSlowSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10, SubscriberBuffer: 1})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	for range 4 {
		b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
			ID:        "audit",
			RequestID: "req",
			Timestamp: time.Now(),
		})
	}

	received := 0
	for {
		select {
		case _, ok := <-sub.Events:
			if !ok {
				require.NotEqual(t, 0, received)

				return
			}
			received++
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for slow subscriber to close")
		}
	}
}

func TestBrokerCloseStopsSubscribersAndRejectsNewSubscriptions(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	b.Close()

	select {
	case _, ok := <-sub.Events:
		require.False(t, ok)

	case <-time.After(time.Second):
		t.Fatal("subscriber channel was not closed")
	}
	require.False(t, b.Enabled())
	got := b.Subscribe(0)
	require.Nil(t, got)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-closed",
		RequestID: "req-closed",
		Timestamp: time.Now(),
	})
	require.Zero(t, b.LatestSeq(), "LatestSeq() after close publish")

	sub.Close()
	b.Close()
}

func TestBrokerAuditStartedPreviewIncludesRequestHeadersOnly(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			UserAgent:       "test-agent",
			APIKeyHash:      "hash123",
			RequestHeaders:  map[string]string{"Authorization": "[REDACTED]", "Content-Type": "application/json"},
			ResponseHeaders: map[string]string{"X-Request-ID": "req-1"},
			RequestBody:     map[string]any{"model": "gpt-test"},
			ResponseBody:    map[string]any{"id": "chatcmpl-test"},
		},
	})

	payload := eventPayload(t, b.events[0])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])

	headers, ok := data["request_headers"].(map[string]any)
	require.True(t, ok, "request_headers = %T, want object", data["request_headers"])
	got := headers["Authorization"]
	require.Equal(t, "[REDACTED]", got)
	got = data["user_agent"]
	require.Equal(t, "test-agent", got)
	got = data["api_key_hash"]
	require.Equal(t, "hash123", got)
	_, ok = data["request_body"]
	require.False(t, ok)
	_, ok = data["response_headers"]
	require.False(t, ok)
	_, ok = data["response_body"]
	require.False(t, ok)
}

func TestBrokerAuditActiveSnapshotMergesNestedPreviewData(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Data: &auditlog.LogData{
			RequestHeaders: map[string]string{"Authorization": "[REDACTED]"},
		},
	})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
		Data: &auditlog.LogData{
			WorkflowFeatures: &auditlog.WorkflowFeaturesSnapshot{Audit: true, Usage: true},
		},
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Len(t, sub.Replay, 1)

	payload := eventPayload(t, sub.Replay[0])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])
	headers, ok := data["request_headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", headers["Authorization"], "request_headers = %#v, want redacted authorization", data["request_headers"])
	features, ok := data["workflow_features"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, features["audit"])
	require.Equal(t, true, features["usage"], "workflow_features = %#v, want audit and usage flags", data["workflow_features"])
}

func TestBrokerAuditUpdatedPreviewIncludesRequestBodyOnly(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  time.Now(),
		StatusCode: 200,
		Data: &auditlog.LogData{
			RequestHeaders:  map[string]string{"Authorization": "[REDACTED]"},
			RequestBody:     map[string]any{"model": "gpt-test"},
			ResponseHeaders: map[string]string{"X-Request-ID": "req-1"},
			ResponseBody:    map[string]any{"id": "chatcmpl-test"},
		},
	})

	// Connected subscribers receive the request body live.
	payload := eventPayload(t, <-sub.Events)
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])
	body, ok := data["request_body"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-test", body["model"], "request_body = %#v, want model", data["request_body"])
	_, ok = data["response_headers"]
	require.False(t, ok)
	_, ok = data["response_body"]
	require.False(t, ok)

	// The replay ring retains the preview without the body, flagged as captured.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	require.True(t, ok, "retained preview data = %T, want object", retained["data"])
	_, ok = retainedData["request_body"]
	require.False(t, ok)
	require.Equal(t, true, retainedData["request_body_captured"])
	headers, ok := retainedData["request_headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", headers["Authorization"], "retained request_headers = %#v, want redacted authorization", retainedData["request_headers"])
}

func TestBrokerAuditCompletedPreviewIncludesCapturedDetailData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	entry := &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  time.Now(),
		StatusCode: 200,
		Data: &auditlog.LogData{
			RequestHeaders:  map[string]string{"Authorization": "Bearer redacted"},
			ResponseHeaders: map[string]string{"X-Request-ID": "req-1"},
			RequestBody:     map[string]any{"model": "gpt-test", "nested": map[string]any{"token": "before"}},
			ResponseBody:    map[string]any{"id": "chatcmpl-test", "usage": map[string]any{"total_tokens": 150}},
		},
	}
	b.PublishAuditEvent(EventAuditCompleted, entry)
	entry.Data.RequestHeaders["Authorization"] = "Bearer changed"
	entry.Data.ResponseHeaders["X-Request-ID"] = "changed"
	entry.Data.RequestBody.(map[string]any)["model"] = "changed"
	entry.Data.RequestBody.(map[string]any)["nested"].(map[string]any)["token"] = "changed"
	entry.Data.ResponseBody.(map[string]any)["id"] = "changed"
	entry.Data.ResponseBody.(map[string]any)["usage"].(map[string]any)["total_tokens"] = 999

	// Connected subscribers receive the full captured detail, decoupled from
	// later entry mutations.
	payload := eventPayload(t, <-sub.Events)
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])
	headers, ok := data["request_headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Bearer redacted", headers["Authorization"], "request_headers = %#v, want redacted authorization", data["request_headers"])
	headers, ok = data["response_headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req-1", headers["X-Request-ID"], "response_headers = %#v, want x-request-id", data["response_headers"])
	body, ok := data["request_body"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-test", body["model"], "request_body = %#v, want model", data["request_body"])
	nested, ok := body["nested"].(map[string]any)
	require.True(t, ok, "request_body nested = %#v, want object", body["nested"])
	require.Equal(t, "before", nested["token"], "request_body nested = %#v, want original nested token", nested)
	body, ok = data["response_body"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "chatcmpl-test", body["id"], "response_body = %#v, want response id", data["response_body"])
	usage, ok := body["usage"].(map[string]any)
	require.True(t, ok, "response_body usage = %#v, want object", body["usage"])
	require.Equal(t, float64(150), usage["total_tokens"], "response_body usage = %#v, want original usage", usage)

	// The replay ring keeps the detail without bodies, flagged as captured.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	require.True(t, ok, "retained preview data = %T, want object", retained["data"])
	_, ok = retainedData["request_body"]
	require.False(t, ok)
	_, ok = retainedData["response_body"]
	require.False(t, ok)
	require.Equal(t, true, retainedData["request_body_captured"])
	require.Equal(t, true, retainedData["response_body_captured"])
	headers, ok = retainedData["response_headers"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req-1", headers["X-Request-ID"], "retained response_headers = %#v, want x-request-id", retainedData["response_headers"])
}

func TestBrokerAuditPreviewIncludesCompactWorkflowData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			WorkflowFeatures: &auditlog.WorkflowFeaturesSnapshot{
				Cache:    true,
				Audit:    true,
				Usage:    true,
				Failover: true,
			},
			Failover: &auditlog.FailoverSnapshot{TargetModel: "fallback-model"},
		},
	})

	payload := eventPayload(t, b.events[0])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])

	features, ok := data["workflow_features"].(map[string]any)
	require.True(t, ok, "workflow_features = %T, want object", data["workflow_features"])
	require.Equal(t, true, features["cache"])
	require.Equal(t, true, features["failover"], "workflow_features = %#v, want compact workflow flags", features)

	failover, ok := data["failover"].(map[string]any)
	require.True(t, ok, "failover = %T, want object", data["failover"])
	require.Equal(t, "fallback-model", failover["target_model"])
}

func TestBrokerAuditPreviewIncludesCompactAttempts(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			Attempts: []auditlog.AttemptSnapshot{
				{Seq: 1, Kind: auditlog.AttemptKindPrimary, StatusCode: 404, ErrorMessage: "model not available",
					ResponseBody:    map[string]any{"error": "nope"},
					ResponseHeaders: map[string]string{"Retry-After": "30"}},
				{Seq: 2, Kind: auditlog.AttemptKindFailover, StatusCode: 200, Success: true},
			},
		},
	})

	payload := eventPayload(t, b.events[0])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])

	attempts, ok := data["attempts"].([]any)
	require.True(t, ok)
	require.Len(t, attempts, 2)

	primary, ok := attempts[0].(map[string]any)
	require.True(t, ok, "attempt[0] = %T, want object", attempts[0])
	require.Equal(t, auditlog.AttemptKindPrimary, primary["kind"])
	require.Equal(t, float64(404), primary["status_code"], "attempt[0] = %#v, want failed primary metadata", primary)
	_, present := primary["response_body"]
	require.False(t, present, "live preview attempt should omit response_body, got %#v", primary)
	_, present = primary["response_headers"]
	require.False(t, present, "live preview attempt should omit response_headers, got %#v", primary)
}

func TestBrokerAuditPreviewIncludesGuardrailOutcomesWithoutDetail(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			Guardrails: []auditlog.GuardrailOutcomeSnapshot{
				{Seq: 1, Phase: "prompt", Instance: "check", Action: auditlog.GuardrailActionBlock, Code: "policy",
					Detail: map[string]any{"verdict": "unsafe"}},
			},
		},
	})

	payload := eventPayload(t, b.events[0])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])

	outcomes, ok := data["guardrails"].([]any)
	require.True(t, ok)
	require.Len(t, outcomes, 1, "guardrails = %#v, want 1 outcome in the live preview", data["guardrails"])

	outcome, ok := outcomes[0].(map[string]any)
	require.True(t, ok, "guardrails[0] = %T, want object", outcomes[0])
	require.Equal(t, "check", outcome["instance"])
	require.Equal(t, auditlog.GuardrailActionBlock, outcome["action"])
	require.Equal(t, "policy", outcome["code"], "guardrails[0] = %#v, want the block outcome", outcome)
	_, present := outcome["detail"]
	require.False(t, present, "live preview outcome should omit detail, got %#v", outcome)
}

func TestAuditPreviewRemainsPendingUntilFlush(t *testing.T) {
	entry := &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	}

	queued := auditPreviewFromEntry(EventAuditCompleted, entry)
	require.True(t, queued.LivePending)

	flushed := auditPreviewFromEntry(EventAuditFlushed, entry)
	require.False(t, flushed.LivePending)

	failed := auditPreviewFromEntry(EventAuditFailed, entry)
	require.False(t, failed.LivePending)
}

func TestUsagePreviewIncludesRawData(t *testing.T) {
	rawData := map[string]any{
		"prompt_cached_tokens": 150,
		"details": map[string]any{
			"cache_read_tokens": 125,
		},
		"segments": []any{map[string]any{"kind": "cached"}},
	}
	preview := usagePreviewFromEntry(&usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		RawData:   rawData,
	})

	require.Equal(t, 150, preview.RawData["prompt_cached_tokens"])

	rawData["prompt_cached_tokens"] = 200
	require.Equal(t, 150, preview.RawData["prompt_cached_tokens"])

	rawData["details"].(map[string]any)["cache_read_tokens"] = 999
	details, ok := preview.RawData["details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 125, details["cache_read_tokens"], "raw_data details = %#v, want original nested details", preview.RawData["details"])

	rawData["segments"].([]any)[0].(map[string]any)["kind"] = "changed"
	segments, ok := preview.RawData["segments"].([]any)
	require.True(t, ok)
	require.Len(t, segments, 1)
	segment, ok := segments[0].(map[string]any)
	require.True(t, ok, "segments[0] = %T, want object", segments[0])
	require.Equal(t, "cached", segment["kind"])
}

func TestBrokerRingCapacityBoundedByReplayWindow(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10000, ReplayLimit: 100})
	require.Equal(t, 101, b.bufferSize)

	// A buffer smaller than the replay window is kept as configured.
	b = NewBroker(Config{Enabled: true, BufferSize: 50, ReplayLimit: 100})
	require.Equal(t, 50, b.bufferSize)
}

func TestBrokerActiveSnapshotsExcludeBodiesForFreshSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			RequestBody: map[string]any{"model": "gpt-test"},
		},
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	require.Len(t, sub.Replay, 1)

	data, ok := eventPayload(t, sub.Replay[0])["data"].(map[string]any)
	require.True(t, ok)
	_, ok = data["request_body"]
	require.False(t, ok)
	require.Equal(t, true, data["request_body_captured"])
}

func TestBrokerCompactsOversizedRetainedEvents(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	oversized := make(map[string]string, 1)
	headerValue := make([]byte, maxRetainedEventBytes)
	for i := range headerValue {
		headerValue[i] = 'x'
	}
	oversized["X-Big"] = string(headerValue)
	b.PublishAuditEvent(EventAuditCompleted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Data: &auditlog.LogData{
			RequestHeaders: oversized,
			RequestBody:    map[string]any{"model": "gpt-test"},
		},
	})

	retained := b.events[0]
	require.LessOrEqual(t, len(retained.Data), maxRetainedEventBytes)

	data, ok := eventPayload(t, retained)["data"].(map[string]any)
	require.True(t, ok)
	_, ok = data["request_headers"]
	require.False(t, ok)
	require.Equal(t, true, data["request_body_captured"])
}

func TestBrokerAuditStreamPreviewCarriesPartialResponseBody(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()

	b.PublishAuditEvent(EventAuditStream, &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  time.Now(),
		StatusCode: 200,
		Stream:     true,
		Data: &auditlog.LogData{
			ResponseBody: map[string]any{
				"object": "chat.completion",
				"choices": []any{map[string]any{
					"index":   0,
					"message": map[string]any{"role": "assistant", "content": "partial"},
				}},
			},
		},
	})

	// Connected subscribers receive the partial body, flagged as partial and
	// still pending.
	payload := eventPayload(t, <-sub.Events)
	require.Equal(t, EventAuditStream, payload["_live_state"])
	require.Equal(t, true, payload["_live_pending"])

	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])
	_, ok = data["response_body"].(map[string]any)
	require.True(t, ok, "response_body = %#v, want partial body", data["response_body"])
	require.Equal(t, true, data["response_body_partial"])

	// The replay ring drops the partial body without claiming it was captured
	// (it is not in the persisted entry yet) and without the partial flag,
	// which would go stale in merged active snapshots.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	require.True(t, ok, "retained preview data = %T, want object", retained["data"])
	_, ok = retainedData["response_body"]
	require.False(t, ok)
	_, ok = retainedData["response_body_captured"]
	require.False(t, ok)
	_, ok = retainedData["response_body_partial"]
	require.False(t, ok)
}

func TestBrokerHasLiveSubscribers(t *testing.T) {
	var nilBroker *Broker
	require.False(t, nilBroker.HasLiveSubscribers())
	require.False(t, NewBroker(Config{}).HasLiveSubscribers())

	b := NewBroker(Config{Enabled: true})
	require.False(t, b.HasLiveSubscribers())

	sub := b.Subscribe(0)
	require.NotNil(t, sub)
	require.True(t, b.HasLiveSubscribers())

	sub.Close()
	require.False(t, b.HasLiveSubscribers())
}

func eventPayload(t *testing.T, event Event) map[string]any {
	t.Helper()
	var payload map[string]any
	err := json.Unmarshal(event.Data, &payload)
	require.NoError(t, err)

	return payload
}

// BenchmarkBrokerPublishSteadyState measures publish cost once the replay
// buffer is full — the steady state under sustained traffic.
func BenchmarkBrokerPublishSteadyState(b *testing.B) {
	broker := NewBroker(Config{Enabled: true, BufferSize: 10000, ReplayLimit: 1000})
	entry := &usage.UsageEntry{
		ID:        "usage-bench",
		RequestID: "req-bench",
		Timestamp: time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
		Model:     "gpt-test",
		Provider:  "openai",
	}
	for range 10001 {
		broker.PublishUsageEvent(EventUsageFlushed, entry)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		broker.PublishUsageEvent(EventUsageFlushed, entry)
	}
}

// TestBrokerReplayAfterBufferWrap fills the circular replay buffer past
// capacity so the head index has advanced, then checks replay content and
// ordering for cursors inside and outside the retained window.
func TestBrokerReplayAfterBufferWrap(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 4, ReplayLimit: 4})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// Publish 6 usage-flushed events (seq 1..6); the buffer retains seq 3..6
	// with the ring head pointing mid-slice.
	for i := range 6 {
		b.PublishUsageEvent(EventUsageFlushed, &usage.UsageEntry{
			ID:        "usage-wrap",
			RequestID: "req-wrap",
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	sub := b.Subscribe(4)
	require.NotNil(t, sub)

	defer sub.Close()
	require.False(t, sub.Reset)
	require.Len(t, sub.Replay, 2)

	for i, wantSeq := range []uint64{5, 6} {
		got := sub.Replay[i].Seq
		require.Equal(t, wantSeq, got, "replay[%d].Seq = %d, want %d", i, got, wantSeq)
	}

	// Cursor exactly one before the oldest retained event replays the whole window.
	subFull := b.Subscribe(2)
	require.NotNil(t, subFull)

	defer subFull.Close()
	require.False(t, subFull.Reset)
	require.Len(t, subFull.Replay, 4)

	for i, wantSeq := range []uint64{3, 4, 5, 6} {
		got := subFull.Replay[i].Seq
		require.Equal(t, wantSeq, got, "full replay[%d].Seq = %d, want %d", i, got, wantSeq)
	}

	// A cursor older than the retained window resets to active snapshots.
	subStale := b.Subscribe(1)
	require.NotNil(t, subStale)

	defer subStale.Close()
	require.True(t, subStale.Reset)
}

// benchAuditRequestLifecycle publishes the audit events one non-streaming chat
// request emits with body and header capture on: audit.started, the
// audit.updated events handlers raise while enriching the entry, then
// audit.completed and audit.flushed. It is the per-request broker cost on
// every request, whether or not a dashboard is connected.
func benchAuditRequestLifecycle(b *testing.B, subscribe bool) {
	// The subscriber buffer is far larger than the default so a drain
	// goroutine that falls behind cannot get the subscriber dropped, which
	// would silently turn the subscribed case into the idle one.
	broker := NewBroker(Config{Enabled: true, BufferSize: 10000, ReplayLimit: 1000, SubscriberBuffer: 1 << 16})
	if subscribe {
		sub := broker.Subscribe(0)
		require.NotNil(b, sub)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range sub.Events {
			}
		}()
		b.Cleanup(func() {
			sub.Close()
			<-done
		})
	}

	requestBody := auditlog.CaptureLoggedBody([]byte(`{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are a helpful assistant that answers briefly."},{"role":"user","content":"Summarise the attached incident report in three bullet points and suggest one follow-up action for the on-call engineer."}],"temperature":0.2,"max_tokens":256,"stream":false}`))
	responseBody := auditlog.CaptureLoggedBody([]byte(`{"id":"chatcmpl-bench","object":"chat.completion","created":1700000000,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"- Database failover took 4 minutes\n- Alerts fired late\n- No data loss\nFollow-up: tune the replica lag alert."},"finish_reason":"stop"}],"usage":{"prompt_tokens":61,"completion_tokens":34,"total_tokens":95}}`))
	requestHeaders := map[string]string{"Authorization": "[REDACTED]", "Content-Type": "application/json", "User-Agent": "openai-python/1.60.0"}
	responseHeaders := map[string]string{"Content-Type": "application/json", "X-Request-Id": "req-bench"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := &auditlog.LogEntry{
			ID:        "audit-bench",
			RequestID: "req-bench",
			Timestamp: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
			Method:    "POST",
			Path:      "/v1/chat/completions",
			Data: &auditlog.LogData{
				UserAgent:      "openai-python/1.60.0",
				RequestHeaders: requestHeaders,
				RequestBody:    requestBody,
			},
		}
		broker.PublishAuditEvent(EventAuditStarted, entry)

		entry.RequestedModel, entry.Provider = "gpt-4o-mini", "openai"
		broker.PublishAuditEvent(EventAuditUpdated, entry)
		entry.AuthMethod = "master_key"
		broker.PublishAuditEvent(EventAuditUpdated, entry)
		entry.ResolvedModel, entry.ProviderName = "openai/gpt-4o-mini", "openai"
		broker.PublishAuditEvent(EventAuditUpdated, entry)
		entry.UserPath = "/team"
		broker.PublishAuditEvent(EventAuditUpdated, entry)

		entry.StatusCode, entry.DurationNs = 200, 812_000_000
		entry.Data.ResponseHeaders = responseHeaders
		entry.Data.ResponseBody = responseBody
		broker.PublishAuditEvent(EventAuditCompleted, entry)
		broker.PublishAuditEvent(EventAuditFlushed, entry)
	}
	b.StopTimer()
	if subscribe && !broker.HasLiveSubscribers() {
		b.Fatal("the subscriber was dropped during the run, so the idle path was measured")
	}
}

func BenchmarkBrokerAuditRequestLifecycleNoSubscribers(b *testing.B) {
	benchAuditRequestLifecycle(b, false)
}

func BenchmarkBrokerAuditRequestLifecycleWithSubscriber(b *testing.B) {
	benchAuditRequestLifecycle(b, true)
}

// An event reports only what its type carries, so the accumulated snapshot
// must keep what earlier events established rather than let a later event's
// zero fields erase it.
func TestBrokerAuditActiveSnapshotKeepsEarlierFields(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Method:    "POST",
		Path:      "/v1/chat/completions",
		Stream:    true,
		Data: &auditlog.LogData{
			UserAgent:      "openai-python/1.60.0",
			RequestHeaders: map[string]string{"Authorization": "[REDACTED]", "Content-Type": "application/json"},
		},
	})
	b.PublishAuditEvent(EventAuditCompleted, &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  now.Add(time.Second),
		StatusCode: 200,
		Data: &auditlog.LogData{
			ResponseHeaders: map[string]string{"X-Request-Id": "req-1"},
			RequestHeaders:  map[string]string{"Content-Type": "application/json; charset=utf-8"},
		},
	})

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Len(t, sub.Replay, 1)

	payload := eventPayload(t, sub.Replay[0])
	assert.Equal(t, "POST", payload["method"], "method from audit.started")
	assert.Equal(t, "/v1/chat/completions", payload["path"])
	assert.Equal(t, true, payload["stream"])
	assert.Equal(t, float64(200), payload["status_code"], "status code from audit.completed")
	assert.Equal(t, EventAuditCompleted, payload["_live_state"], "the newest event names the state")

	data, ok := payload["data"].(map[string]any)
	require.True(t, ok, "preview data = %T, want object", payload["data"])
	assert.Equal(t, "openai-python/1.60.0", data["user_agent"])
	requestHeaders, ok := data["request_headers"].(map[string]any)
	require.True(t, ok, "request_headers = %T, want object", data["request_headers"])
	assert.Equal(t, "[REDACTED]", requestHeaders["Authorization"], "a header only the first event carried")
	assert.Equal(t, "application/json; charset=utf-8", requestHeaders["Content-Type"], "the later event wins on conflict")
	responseHeaders, ok := data["response_headers"].(map[string]any)
	require.True(t, ok, "response_headers = %T, want object", data["response_headers"])
	assert.Equal(t, "req-1", responseHeaders["X-Request-Id"])
}

// Snapshots outlive the request, so they must not alias maps the audit entry
// goes on mutating.
func TestBrokerAuditActiveSnapshotDoesNotAliasEntryData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	headers := map[string]string{"Authorization": "[REDACTED]"}

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now,
		Data:      &auditlog.LogData{RequestHeaders: headers},
	})
	b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
		Provider:  "openai",
	})
	headers["Authorization"] = "sk-leaked"
	headers["X-Added-Later"] = "1"

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Len(t, sub.Replay, 1)

	data, ok := eventPayload(t, sub.Replay[0])["data"].(map[string]any)
	require.True(t, ok)
	requestHeaders, ok := data["request_headers"].(map[string]any)
	require.True(t, ok, "request_headers = %T, want object", data["request_headers"])
	assert.Equal(t, "[REDACTED]", requestHeaders["Authorization"], "the snapshot must hold the value published, not a later mutation")
	assert.NotContains(t, requestHeaders, "X-Added-Later")
}

// The retained-size cap bounds the ring, so it has to hold for the accumulated
// snapshot and not only for the event that produced it.
func TestBrokerCapsRetainedSizeAfterMerge(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	half := strings.Repeat("x", maxRetainedEventBytes*2/3)

	for i, header := range []string{"X-First", "X-Second"} {
		b.PublishAuditEvent(EventAuditUpdated, &auditlog.LogEntry{
			ID:        "audit-1",
			RequestID: "req-1",
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Data:      &auditlog.LogData{RequestHeaders: map[string]string{header: half}},
		})
	}

	sub := b.Subscribe(0)
	require.NotNil(t, sub)

	defer sub.Close()
	require.Len(t, sub.Replay, 1)
	assert.LessOrEqual(t, len(sub.Replay[0].Data), maxRetainedEventBytes, "merged snapshot exceeds the retention cap")
}

// Compaction empties the preview's data, but a single oversized top-level
// field can still carry the event past the retention cap.
func TestBrokerCapsRetainedEventsWithOversizedErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		size int
	}{
		{name: "just over the cap", size: maxRetainedEventBytes + 1},
		{name: "many times the cap", size: 3 * maxRetainedEventBytes},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBroker(Config{Enabled: true})

			b.PublishAuditEvent(EventAuditFailed, &auditlog.LogEntry{
				ID:        "audit-1",
				RequestID: "req-1",
				Timestamp: time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
				Data:      &auditlog.LogData{ErrorMessage: strings.Repeat("e", tc.size)},
			})

			require.Len(t, b.events, 1)
			assert.LessOrEqual(t, len(b.events[0].Data), maxRetainedEventBytes, "retained event exceeds the cap")

			message, ok := eventPayload(t, b.events[0])["error_message"].(string)
			require.True(t, ok, "error_message missing from the retained event")
			assert.True(t, strings.HasSuffix(message, retainedTextTruncationMarker), "a truncated message should say so, got %q", message[max(0, len(message)-40):])
		})
	}
}
