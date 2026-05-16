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
	for range sub.Events {
		received++
	}
	if received == 0 {
		t.Fatal("slow subscriber received no buffered event before close")
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

func TestBrokerAuditPreviewOmitsAdvancedData(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	b.PublishAuditEvent(EventAuditCompleted, &auditlog.LogEntry{
		ID:         "audit-1",
		RequestID:  "req-1",
		Timestamp:  time.Now(),
		StatusCode: 200,
		Data: &auditlog.LogData{
			RequestBody: map[string]any{"secret": "large body"},
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
	if _, ok := payload["data"]; ok {
		t.Fatal("preview payload contains advanced data")
	}
	if _, ok := payload["request_body"]; ok {
		t.Fatal("preview payload contains request body")
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
			Failover:    &auditlog.FailoverSnapshot{TargetModel: "fallback-model"},
			RequestBody: map[string]any{"messages": []any{"sensitive"}},
		},
	})

	payload := eventPayload(t, b.events[0])
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("preview data = %T, want object", payload["data"])
	}
	if _, ok := data["request_body"]; ok {
		t.Fatal("preview data contains request body")
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
	rawData := map[string]any{"prompt_cached_tokens": 150}
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
}

func eventPayload(t *testing.T, event Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	return payload
}
