package live

import (
	"encoding/json"
	"testing"
	"time"

	"gomodel/internal/auditlog"
	"gomodel/internal/usage"
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	if sub.Reset {
		t.Fatal("Subscribe reset = true, want false")
	}
	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}
	if got := sub.Replay[0].Type; got != EventUsageCompleted {
		t.Fatalf("replay type = %q, want %q", got, EventUsageCompleted)
	}
	if got := sub.Replay[0].Seq; got != 2 {
		t.Fatalf("replay seq = %d, want 2", got)
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

	if got := b.LatestSeq(); got != 2 {
		t.Fatalf("latest seq = %d, want 2", got)
	}

	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if len(sub.Replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(sub.Replay))
	}
	for i, event := range sub.Replay {
		if event.Type != EventUsageCompleted {
			t.Fatalf("replay[%d] type = %q, want %q", i, event.Type, EventUsageCompleted)
		}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	if sub.Reset {
		t.Fatal("Subscribe reset = true, want false")
	}
	if len(sub.Replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(sub.Replay))
	}
	if got := sub.Replay[0].Type; got != EventAuditUpdated {
		t.Fatalf("audit snapshot type = %q, want %q", got, EventAuditUpdated)
	}
	payload := eventPayload(t, sub.Replay[0])
	if got := payload["method"]; got != "POST" {
		t.Fatalf("snapshot method = %v, want POST", got)
	}
	if got := payload["provider"]; got != "openai" {
		t.Fatalf("snapshot provider = %v, want openai", got)
	}
	if got := sub.Replay[1].Type; got != EventUsageCompleted {
		t.Fatalf("usage snapshot type = %q, want %q", got, EventUsageCompleted)
	}
}

func TestBrokerNormalizesAuditActiveSnapshotAliases(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	b.publish(EventAuditUpdated, "audit-1", "", now, map[string]any{
		"id":     "audit-1",
		"method": "POST",
	})
	b.publish(EventAuditUpdated, "audit-1", "req-1", now.Add(time.Second), map[string]any{
		"id":         "audit-1",
		"request_id": "req-1",
		"provider":   "openai",
	})

	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}
	payload := eventPayload(t, sub.Replay[0])
	if got := payload["method"]; got != "POST" {
		t.Fatalf("snapshot method = %v, want POST", got)
	}
	if got := payload["provider"]; got != "openai" {
		t.Fatalf("snapshot provider = %v, want openai", got)
	}

	b.publish(EventAuditFlushed, "audit-1", "req-1", now.Add(2*time.Second), map[string]any{
		"id":         "audit-1",
		"request_id": "req-1",
	})
	subAfterFlush := b.Subscribe(0)
	if subAfterFlush == nil {
		t.Fatal("Subscribe returned nil after flush")
	}
	defer subAfterFlush.Close()
	if len(subAfterFlush.Replay) != 0 {
		t.Fatalf("post-flush replay len = %d, want 0", len(subAfterFlush.Replay))
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}
	payload := eventPayload(t, sub.Replay[0])
	if got := payload["total_tokens"]; got != float64(14) {
		t.Fatalf("snapshot total_tokens = %v, want 14", got)
	}
	if got := payload["model"]; got != "gpt-test" {
		t.Fatalf("snapshot model = %v, want gpt-test", got)
	}

	b.publish(EventUsageFlushed, "usage-1", "req-1", now.Add(2*time.Second), map[string]any{
		"id":         "usage-1",
		"request_id": "req-1",
	})
	subAfterFlush := b.Subscribe(0)
	if subAfterFlush == nil {
		t.Fatal("Subscribe returned nil after flush")
	}
	defer subAfterFlush.Close()
	if len(subAfterFlush.Replay) != 0 {
		t.Fatalf("post-flush replay len = %d, want 0", len(subAfterFlush.Replay))
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if len(sub.Replay) != 0 {
		t.Fatalf("replay len = %d, want 0", len(sub.Replay))
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if !sub.Reset {
		t.Fatal("Subscribe reset = false, want true")
	}
	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}
	if got := sub.Replay[0].Seq; got != 3 {
		t.Fatalf("snapshot seq = %d, want 3", got)
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if !sub.Reset {
		t.Fatal("Subscribe reset = false, want true")
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if !sub.Reset {
		t.Fatal("Subscribe reset = false, want true")
	}
	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want active snapshot only", len(sub.Replay))
	}
	if got := sub.Replay[0].Seq; got != 5 {
		t.Fatalf("snapshot seq = %d, want 5", got)
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if !sub.Reset {
		t.Fatal("Subscribe reset = false, want true")
	}
	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want active snapshot only", len(sub.Replay))
	}
	if got := sub.Replay[0].Seq; got != 1 {
		t.Fatalf("snapshot seq = %d, want 1", got)
	}
}

func TestBrokerDropsSlowSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10, SubscriberBuffer: 1})
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
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
				if received == 0 {
					t.Fatal("slow subscriber received no buffered event before close")
				}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}

	b.Close()

	select {
	case _, ok := <-sub.Events:
		if ok {
			t.Fatal("subscriber channel remained open after broker close")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel was not closed")
	}
	if b.Enabled() {
		t.Fatal("Enabled() = true after broker close, want false")
	}
	if got := b.Subscribe(0); got != nil {
		t.Fatal("Subscribe returned a subscription after broker close")
	}

	b.PublishAuditEvent(EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-closed",
		RequestID: "req-closed",
		Timestamp: time.Now(),
	})
	if got := b.LatestSeq(); got != 0 {
		t.Fatalf("LatestSeq() = %d after close publish, want 0", got)
	}

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
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	headers, ok := data["request_headers"].(map[string]any)
	if !ok {
		t.Fatalf("request_headers = %T, want object", data["request_headers"])
	}
	if got := headers["Authorization"]; got != "[REDACTED]" {
		t.Fatalf("request_headers[Authorization] = %v, want [REDACTED]", got)
	}
	if got := data["user_agent"]; got != "test-agent" {
		t.Fatalf("user_agent = %v, want test-agent", got)
	}
	if got := data["api_key_hash"]; got != "hash123" {
		t.Fatalf("api_key_hash = %v, want hash123", got)
	}
	if _, ok := data["request_body"]; ok {
		t.Fatal("started preview data contains request body")
	}
	if _, ok := data["response_headers"]; ok {
		t.Fatal("started preview data contains response headers")
	}
	if _, ok := data["response_body"]; ok {
		t.Fatal("started preview data contains response body")
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}

	payload := eventPayload(t, sub.Replay[0])
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	if headers, ok := data["request_headers"].(map[string]any); !ok || headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("request_headers = %#v, want redacted authorization", data["request_headers"])
	}
	if features, ok := data["workflow_features"].(map[string]any); !ok || features["audit"] != true || features["usage"] != true {
		t.Fatalf("workflow_features = %#v, want audit and usage flags", data["workflow_features"])
	}
}

func TestBrokerAuditUpdatedPreviewIncludesRequestBodyOnly(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
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
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	if body, ok := data["request_body"].(map[string]any); !ok || body["model"] != "gpt-test" {
		t.Fatalf("request_body = %#v, want model", data["request_body"])
	}
	if _, ok := data["response_headers"]; ok {
		t.Fatal("updated preview data contains response headers")
	}
	if _, ok := data["response_body"]; ok {
		t.Fatal("updated preview data contains response body")
	}

	// The replay ring retains the preview without the body, flagged as captured.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	if !ok {
		t.Fatalf("retained preview data = %T, want object", retained["data"])
	}
	if _, ok := retainedData["request_body"]; ok {
		t.Fatal("retained preview contains request body")
	}
	if retainedData["request_body_captured"] != true {
		t.Fatalf("request_body_captured = %#v, want true", retainedData["request_body_captured"])
	}
	if headers, ok := retainedData["request_headers"].(map[string]any); !ok || headers["Authorization"] != "[REDACTED]" {
		t.Fatalf("retained request_headers = %#v, want redacted authorization", retainedData["request_headers"])
	}
}

func TestBrokerAuditCompletedPreviewIncludesCapturedDetailData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
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
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	if headers, ok := data["request_headers"].(map[string]any); !ok || headers["Authorization"] != "Bearer redacted" {
		t.Fatalf("request_headers = %#v, want redacted authorization", data["request_headers"])
	}
	if headers, ok := data["response_headers"].(map[string]any); !ok || headers["X-Request-ID"] != "req-1" {
		t.Fatalf("response_headers = %#v, want x-request-id", data["response_headers"])
	}
	if body, ok := data["request_body"].(map[string]any); !ok || body["model"] != "gpt-test" {
		t.Fatalf("request_body = %#v, want model", data["request_body"])
	}
	if body := data["request_body"].(map[string]any); body["nested"].(map[string]any)["token"] != "before" {
		t.Fatalf("request_body nested = %#v, want original nested token", body["nested"])
	}
	if body, ok := data["response_body"].(map[string]any); !ok || body["id"] != "chatcmpl-test" {
		t.Fatalf("response_body = %#v, want response id", data["response_body"])
	}
	if body := data["response_body"].(map[string]any); body["usage"].(map[string]any)["total_tokens"] != float64(150) {
		t.Fatalf("response_body usage = %#v, want original usage", body["usage"])
	}

	// The replay ring keeps the detail without bodies, flagged as captured.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	if !ok {
		t.Fatalf("retained preview data = %T, want object", retained["data"])
	}
	if _, ok := retainedData["request_body"]; ok {
		t.Fatal("retained preview contains request body")
	}
	if _, ok := retainedData["response_body"]; ok {
		t.Fatal("retained preview contains response body")
	}
	if retainedData["request_body_captured"] != true || retainedData["response_body_captured"] != true {
		t.Fatalf("captured flags = %#v/%#v, want true/true",
			retainedData["request_body_captured"], retainedData["response_body_captured"])
	}
	if headers, ok := retainedData["response_headers"].(map[string]any); !ok || headers["X-Request-ID"] != "req-1" {
		t.Fatalf("retained response_headers = %#v, want x-request-id", retainedData["response_headers"])
	}
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
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	features, ok := data["workflow_features"].(map[string]any)
	if !ok {
		t.Fatalf("workflow_features = %T, want object", data["workflow_features"])
	}
	if features["cache"] != true || features["failover"] != true {
		t.Fatalf("workflow_features = %#v, want compact workflow flags", features)
	}
	failover, ok := data["failover"].(map[string]any)
	if !ok {
		t.Fatalf("failover = %T, want object", data["failover"])
	}
	if failover["target_model"] != "fallback-model" {
		t.Fatalf("failover target = %v, want fallback-model", failover["target_model"])
	}
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
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	attempts, ok := data["attempts"].([]any)
	if !ok || len(attempts) != 2 {
		t.Fatalf("attempts = %#v, want 2 compact attempts in the live preview", data["attempts"])
	}
	primary, ok := attempts[0].(map[string]any)
	if !ok {
		t.Fatalf("attempt[0] = %T, want object", attempts[0])
	}
	if primary["kind"] != auditlog.AttemptKindPrimary || primary["status_code"].(float64) != 404 {
		t.Fatalf("attempt[0] = %#v, want failed primary metadata", primary)
	}
	if _, present := primary["response_body"]; present {
		t.Fatalf("live preview attempt should omit response_body, got %#v", primary)
	}
	if _, present := primary["response_headers"]; present {
		t.Fatalf("live preview attempt should omit response_headers, got %#v", primary)
	}
}

func TestAuditPreviewRemainsPendingUntilFlush(t *testing.T) {
	entry := &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	}

	queued := auditPreviewFromEntry(EventAuditCompleted, entry)
	if !queued.LivePending {
		t.Fatal("completed audit preview pending = false, want true until storage flush")
	}

	flushed := auditPreviewFromEntry(EventAuditFlushed, entry)
	if flushed.LivePending {
		t.Fatal("flushed audit preview pending = true, want false")
	}

	failed := auditPreviewFromEntry(EventAuditFailed, entry)
	if failed.LivePending {
		t.Fatal("failed audit preview pending = true, want false")
	}
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

	if preview.RawData["prompt_cached_tokens"] != 150 {
		t.Fatalf("raw_data[prompt_cached_tokens] = %v, want 150", preview.RawData["prompt_cached_tokens"])
	}
	rawData["prompt_cached_tokens"] = 200
	if preview.RawData["prompt_cached_tokens"] != 150 {
		t.Fatalf("raw_data was not copied, got %v", preview.RawData["prompt_cached_tokens"])
	}
	rawData["details"].(map[string]any)["cache_read_tokens"] = 999
	if details, ok := preview.RawData["details"].(map[string]any); !ok || details["cache_read_tokens"] != 125 {
		t.Fatalf("raw_data details = %#v, want original nested details", preview.RawData["details"])
	}
	rawData["segments"].([]any)[0].(map[string]any)["kind"] = "changed"
	if segments, ok := preview.RawData["segments"].([]any); !ok || len(segments) != 1 || segments[0].(map[string]any)["kind"] != "cached" {
		t.Fatalf("raw_data segments = %#v, want original nested segment", preview.RawData["segments"])
	}
}

func TestBrokerRingCapacityBoundedByReplayWindow(t *testing.T) {
	b := NewBroker(Config{Enabled: true, BufferSize: 10000, ReplayLimit: 100})
	if b.bufferSize != 101 {
		t.Fatalf("bufferSize = %d, want 101 (replay limit + 1)", b.bufferSize)
	}

	// A buffer smaller than the replay window is kept as configured.
	b = NewBroker(Config{Enabled: true, BufferSize: 50, ReplayLimit: 100})
	if b.bufferSize != 50 {
		t.Fatalf("bufferSize = %d, want 50", b.bufferSize)
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	if len(sub.Replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(sub.Replay))
	}
	data, ok := eventPayload(t, sub.Replay[0])["data"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot data missing")
	}
	if _, ok := data["request_body"]; ok {
		t.Fatal("active snapshot contains request body")
	}
	if data["request_body_captured"] != true {
		t.Fatalf("request_body_captured = %#v, want true", data["request_body_captured"])
	}
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
	if len(retained.Data) > maxRetainedEventBytes {
		t.Fatalf("retained event size = %d, want <= %d", len(retained.Data), maxRetainedEventBytes)
	}
	data, ok := eventPayload(t, retained)["data"].(map[string]any)
	if !ok {
		t.Fatalf("compacted data missing")
	}
	if _, ok := data["request_headers"]; ok {
		t.Fatal("compacted event still contains oversized headers")
	}
	if data["request_body_captured"] != true {
		t.Fatalf("request_body_captured = %#v, want true", data["request_body_captured"])
	}
}

func TestBrokerAuditStreamPreviewCarriesPartialResponseBody(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
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
	if payload["_live_state"] != EventAuditStream {
		t.Fatalf("_live_state = %#v, want %q", payload["_live_state"], EventAuditStream)
	}
	if payload["_live_pending"] != true {
		t.Fatalf("_live_pending = %#v, want true", payload["_live_pending"])
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	if _, ok := data["response_body"].(map[string]any); !ok {
		t.Fatalf("response_body = %#v, want partial body", data["response_body"])
	}
	if data["response_body_partial"] != true {
		t.Fatalf("response_body_partial = %#v, want true", data["response_body_partial"])
	}

	// The replay ring drops the partial body without claiming it was captured
	// (it is not in the persisted entry yet) and without the partial flag,
	// which would go stale in merged active snapshots.
	retained := eventPayload(t, b.events[0])
	retainedData, ok := retained["data"].(map[string]any)
	if !ok {
		t.Fatalf("retained preview data = %T, want object", retained["data"])
	}
	if _, ok := retainedData["response_body"]; ok {
		t.Fatal("retained preview contains partial response body")
	}
	if _, ok := retainedData["response_body_captured"]; ok {
		t.Fatal("retained preview claims a partial body was captured")
	}
	if _, ok := retainedData["response_body_partial"]; ok {
		t.Fatal("retained preview keeps the partial flag")
	}
}

func TestBrokerHasLiveSubscribers(t *testing.T) {
	var nilBroker *Broker
	if nilBroker.HasLiveSubscribers() {
		t.Fatal("nil broker reports subscribers")
	}
	if NewBroker(Config{}).HasLiveSubscribers() {
		t.Fatal("disabled broker reports subscribers")
	}

	b := NewBroker(Config{Enabled: true})
	if b.HasLiveSubscribers() {
		t.Fatal("fresh broker reports subscribers")
	}
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	if !b.HasLiveSubscribers() {
		t.Fatal("broker with a subscription reports none")
	}
	sub.Close()
	if b.HasLiveSubscribers() {
		t.Fatal("broker reports subscribers after unsubscribe")
	}
}

func eventPayload(t *testing.T, event Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
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
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()
	if sub.Reset {
		t.Fatal("Subscribe reset = true, want false for cursor inside retained window")
	}
	if len(sub.Replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(sub.Replay))
	}
	for i, wantSeq := range []uint64{5, 6} {
		if got := sub.Replay[i].Seq; got != wantSeq {
			t.Fatalf("replay[%d].Seq = %d, want %d", i, got, wantSeq)
		}
	}

	// Cursor exactly one before the oldest retained event replays the whole window.
	subFull := b.Subscribe(2)
	if subFull == nil {
		t.Fatal("Subscribe(2) returned nil")
	}
	defer subFull.Close()
	if subFull.Reset {
		t.Fatal("Subscribe(2) reset = true, want false")
	}
	if len(subFull.Replay) != 4 {
		t.Fatalf("full replay len = %d, want 4", len(subFull.Replay))
	}
	for i, wantSeq := range []uint64{3, 4, 5, 6} {
		if got := subFull.Replay[i].Seq; got != wantSeq {
			t.Fatalf("full replay[%d].Seq = %d, want %d", i, got, wantSeq)
		}
	}

	// A cursor older than the retained window resets to active snapshots.
	subStale := b.Subscribe(1)
	if subStale == nil {
		t.Fatal("Subscribe(1) returned nil")
	}
	defer subStale.Close()
	if !subStale.Reset {
		t.Fatal("Subscribe(1) reset = false, want true for cursor before retained window")
	}
}
