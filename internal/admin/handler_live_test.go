package admin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/usage"
)

func TestLiveCursorRejectsInvalidValue(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true})
	h := NewHandler(nil, nil, WithLiveBroker(broker))
	c, rec := echotest.Get(t, "/admin/live/logs?cursor=bad")
	require.NoError(t, h.LiveLogs(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid cursor")
}

func TestLiveTypeFilterProvidedInvalidTokensMatchNothing(t *testing.T) {
	require.True(t, liveTypeFilter("").matches(live.EventAuditStarted))
	require.True(t, liveTypeFilter("audit").matches(live.EventAuditStarted))
	require.False(t, liveTypeFilter("usage").matches(live.EventAuditStarted))
	require.False(t, liveTypeFilter("foo").matches(live.EventAuditStarted))
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
	require.NotContains(t, body, "event: audit.started")
	require.Contains(t, body, "event: usage.completed")

	body = runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=foo")
	require.NotContains(t, body, "event: audit.")
	require.NotContains(t, body, "event: usage.")
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
	require.Contains(t, body, "event: reset")
	require.Contains(t, body, "event: audit.updated")
	require.Contains(t, body, `"provider":"openai"`)
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
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LiveLogs to exit")
	}
}

func TestLiveLogsWritesImmediateHeartbeat(t *testing.T) {
	// Default heartbeat interval (15s) — anything on the wire before the
	// canceled context returns must have been written eagerly on subscribe.
	cases := []struct {
		name       string
		publish    int
		want       string
		wantAbsent string
	}{
		{
			name:    "fresh broker heartbeat carries no sequence",
			publish: 0,
			want:    "event: heartbeat",
			// Seq 0 events omit the SSE id line entirely.
			wantAbsent: "id:",
		},
		{
			name:    "heartbeat carries the subscription-time sequence",
			publish: 1,
			want:    "id: 1\nevent: heartbeat\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := live.NewBroker(live.Config{Enabled: true})
			for range tc.publish {
				broker.PublishAuditEvent(live.EventAuditStarted, &auditlog.LogEntry{
					ID:        "audit-1",
					RequestID: "req-1",
					Timestamp: time.Now(),
				})
			}

			body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=audit,usage")
			require.Contains(t, body, tc.want)

			if tc.wantAbsent != "" {
				require.NotContains(t, body, tc.wantAbsent)
			}
		})
	}
}

// The SSE frame is encoded by hand; it must stay byte for byte the frame the
// handler wrote with json.Marshal and fmt.Fprintf, which the dashboard parses.
func TestAppendLiveEventFrameMatchesMarshaledFrame(t *testing.T) {
	require.Equal(t, 5, reflect.TypeFor[live.Event]().NumField(),
		"live.Event changed shape; update appendLiveEventFrame and this test")

	ts := time.Date(2026, 9, 15, 12, 0, 0, 123456789, time.UTC)
	// Data is what the broker stores: json.Marshal output, already compact
	// and escaped.
	data, err := json.Marshal(map[string]any{
		"id":   "audit-1",
		"data": map[string]any{"request_headers": map[string]string{"User-Agent": "a<b>&\"c\"\n\u2028"}},
	})
	require.NoError(t, err)
	cases := []struct {
		name  string
		event live.Event
	}{
		{name: "heartbeat without sequence", event: live.Event{Type: live.EventHeartbeat, Timestamp: ts}},
		{name: "heartbeat with sequence", event: live.Event{Seq: 42, Type: live.EventHeartbeat, Timestamp: ts}},
		{name: "audit event", event: live.Event{Seq: 7, Type: live.EventAuditUpdated, RequestID: "req-1", Timestamp: ts, Data: data}},
		{name: "whole-second timestamp", event: live.Event{Seq: 1, Type: live.EventUsageCompleted, Timestamp: ts.Truncate(time.Second), Data: json.RawMessage(`{}`)}},
		{name: "non-UTC timestamp", event: live.Event{Seq: 1, Type: live.EventReset, Timestamp: ts.In(time.FixedZone("CEST", 2*60*60))}},
		{name: "request ID needing escapes", event: live.Event{Seq: 3, Type: live.EventAuditStarted, RequestID: "a\"b\\c<d>&e\n\x01", Timestamp: ts}},
		{name: "non-ASCII request ID", event: live.Event{Seq: 3, Type: live.EventAuditStarted, RequestID: "żółw-\u2028-\xff", Timestamp: ts}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.event)
			require.NoError(t, err)
			want := fmt.Sprintf("event: %s\ndata: %s\n\n", tc.event.Type, payload)
			if tc.event.Seq > 0 {
				want = fmt.Sprintf("id: %d\n", tc.event.Seq) + want
			}

			got, err := appendLiveEventFrame([]byte("previous frame"), tc.event)
			require.NoError(t, err)
			assert.Equal(t, "previous frame"+want, string(got))
		})
	}
}

func BenchmarkAppendLiveEventFrame(b *testing.B) {
	event := live.Event{
		Seq:       12345,
		Type:      live.EventAuditUpdated,
		RequestID: "req-bench",
		Timestamp: time.Date(2026, 9, 15, 12, 0, 0, 123456789, time.UTC),
		Data:      json.RawMessage(`{"id":"audit-bench","request_id":"req-bench","timestamp":"2026-09-15T12:00:00Z","requested_model":"gpt-4o-mini","provider":"openai","method":"POST","path":"/v1/chat/completions","data":{"user_agent":"openai-python/1.60.0","request_headers":{"Authorization":"[REDACTED]","Content-Type":"application/json"}},"_live_state":"audit.updated","_live_pending":true}`),
	}
	var buf []byte
	b.ReportAllocs()
	for b.Loop() {
		var err error
		if buf, err = appendLiveEventFrame(buf[:0], event); err != nil {
			b.Fatal(err)
		}
	}
}

func runLiveLogsWithCanceledContext(t *testing.T, broker *live.Broker, target string) string {
	t.Helper()
	h := NewHandler(nil, nil, WithLiveBroker(broker))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, rec := echotest.Get(t, target)
	c.SetRequest(c.Request().WithContext(ctx))
	require.NoError(t, h.LiveLogs(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
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
