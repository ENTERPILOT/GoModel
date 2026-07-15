package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestLiveCursorRejectsInvalidValue(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true})
	h := NewHandler(nil, nil, WithLiveBroker(broker))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/live/logs?cursor=bad", nil)
	rec := httptest.NewRecorder()

	if err := h.LiveLogs(e.NewContext(req, rec)); err != nil {
		t.Fatalf("LiveLogs returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "invalid cursor") {
		t.Fatalf("response body = %q, want invalid cursor error", rec.Body.String())
	}
}

func TestLiveTypeFilterProvidedInvalidTokensMatchNothing(t *testing.T) {
	if !liveTypeFilter("").matches(live.EventAuditStarted) {
		t.Fatal("empty types filter should match audit events")
	}
	if !liveTypeFilter("audit").matches(live.EventAuditStarted) {
		t.Fatal("audit types filter should match audit events")
	}
	if liveTypeFilter("usage").matches(live.EventAuditStarted) {
		t.Fatal("usage types filter matched audit event")
	}
	if liveTypeFilter("foo").matches(live.EventAuditStarted) {
		t.Fatal("invalid provided types filter matched audit event")
	}
}

func TestLiveLogsAppliesTypeFilterToReplayEvents(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true})
	broker.PublishAuditEvent(live.EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	})
	broker.PublishUsageEvent(live.EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	})

	body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=usage")
	if strings.Contains(body, "event: audit.started") {
		t.Fatalf("body contains filtered audit event: %s", body)
	}
	if !strings.Contains(body, "event: usage.completed") {
		t.Fatalf("body = %q, want usage replay event", body)
	}

	body = runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=foo")
	if strings.Contains(body, "event: audit.") || strings.Contains(body, "event: usage.") {
		t.Fatalf("invalid types filter should match no replay events, got: %s", body)
	}
}

func TestLiveLogsWritesResetAndReplayEvents(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	broker.PublishAuditEvent(live.EventAuditStarted, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Method:    http.MethodPost,
	})
	broker.PublishAuditEvent(live.EventAuditUpdated, &auditlog.LogEntry{
		ID:             "audit-1",
		RequestID:      "req-1",
		Timestamp:      time.Now(),
		RequestedModel: "gpt-test",
	})
	broker.PublishAuditEvent(live.EventAuditUpdated, &auditlog.LogEntry{
		ID:        "audit-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Provider:  "openai",
	})

	body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?cursor=1")
	if !strings.Contains(body, "event: reset") {
		t.Fatalf("body = %q, want reset event", body)
	}
	if !strings.Contains(body, "event: audit.updated") {
		t.Fatalf("body = %q, want replayed audit event", body)
	}
	if !strings.Contains(body, `"provider":"openai"`) {
		t.Fatalf("body = %q, want latest replay payload", body)
	}
}

func TestLiveLogsForwardsEventsAndHeartbeats(t *testing.T) {
	broker := live.NewBroker(live.Config{
		Enabled:   true,
		Heartbeat: time.Millisecond,
	})
	h := NewHandler(nil, nil, WithLiveBroker(broker))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/admin/live/logs?types=usage", nil)
	rec := newLiveSSERecorder()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.LiveLogs(e.NewContext(req, rec))
	}()

	waitForLiveOutput(t, rec, func(body string) bool {
		return rec.statusCode() == http.StatusOK
	})
	broker.PublishUsageEvent(live.EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Model:     "gpt-test",
	})

	waitForLiveOutput(t, rec, func(body string) bool {
		return strings.Contains(body, "event: heartbeat") &&
			strings.Contains(body, "event: usage.completed")
	})

	broker.Close()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("LiveLogs returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LiveLogs to exit")
	}
}

func runLiveLogsWithCanceledContext(t *testing.T, broker *live.Broker, target string) string {
	t.Helper()
	h := NewHandler(nil, nil, WithLiveBroker(broker))
	e := echo.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	if err := h.LiveLogs(e.NewContext(req, rec)); err != nil {
		t.Fatalf("LiveLogs returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	return rec.Body.String()
}

type liveSSERecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	status int
}

func newLiveSSERecorder() *liveSSERecorder {
	return &liveSSERecorder{header: http.Header{}}
}

func (r *liveSSERecorder) Header() http.Header {
	return r.header
}

func (r *liveSSERecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *liveSSERecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *liveSSERecorder) Flush() {}

func (r *liveSSERecorder) bodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (r *liveSSERecorder) statusCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func waitForLiveOutput(t *testing.T, rec *liveSSERecorder, ready func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body := rec.bodyString()
		if ready(body) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for live output; status=%d body=%q", rec.statusCode(), rec.bodyString())
}
