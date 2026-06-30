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

	b.publish(EventAuditUpdated, "", now, map[string]any{
		"id":     "audit-1",
		"method": "POST",
	})
	b.publish(EventAuditUpdated, "req-1", now.Add(time.Second), map[string]any{
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

	b.publish(EventAuditFlushed, "", now.Add(2*time.Second), map[string]any{
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

	b.publish(EventUsageCompleted, "req-1", now, map[string]any{
		"request_id":   "req-1",
		"total_tokens": 14,
	})
	b.publish(EventUsageCompleted, "", now.Add(time.Second), map[string]any{
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

	b.publish(EventUsageFlushed, "", now.Add(2*time.Second), map[string]any{
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

	for i := 0; i < 3; i++ {
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
	for i := 0; i < 3; i++ {
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

	for i := 0; i < 5; i++ {
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

	for i := 0; i < 4; i++ {
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
	sub := b.Subscribe(0)
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	defer sub.Close()

	var payload map[string]any
	if err := json.Unmarshal(b.events[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
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
}

func TestBrokerAuditCompletedPreviewIncludesCapturedDetailData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
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

	payload := eventPayload(t, b.events[0])
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
				Fallback: true,
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
	if features["cache"] != true || features["fallback"] != true {
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

func eventPayload(t *testing.T, event Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	return payload
}
