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
}
