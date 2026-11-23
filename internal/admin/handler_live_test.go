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

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/requestlog"
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
	require.True(t, liveTypeFilter("").matches(live.EventRequestLogStarted))
	require.True(t, liveTypeFilter("request_log").matches(live.EventRequestLogStarted))
	require.True(t, liveTypeFilter("audit").matches(live.EventRequestLogStarted))
	require.False(t, liveTypeFilter("usage").matches(live.EventRequestLogStarted))
	require.False(t, liveTypeFilter("foo").matches(live.EventRequestLogStarted))
}

func TestLiveLogsAppliesTypeFilterToReplayEvents(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true})
	broker.PublishRequestLogEvent(live.EventRequestLogStarted, &requestlog.LogEntry{
		ID:        "request-log-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	})
	broker.PublishUsageEvent(live.EventUsageCompleted, &usage.UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
	})

	body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=usage")
	require.NotContains(t, body, "event: request_log.")
	require.NotContains(t, body, "event: audit.")
	require.Contains(t, body, "event: usage.completed")

	body = runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=foo")
	require.NotContains(t, body, "event: request_log.")
	require.NotContains(t, body, "event: audit.")
	require.NotContains(t, body, "event: usage.")
}

// A subscriber gets request log events under the names it asked for: the
// request_log.* ones when it lists request_log, and the deprecated audit.*
// ones when it lists only the audit alias or passes no types, as clients
// written before the rename do. The name is the same in the SSE event field,
// the envelope and the preview's _live_state.
func TestLiveLogsNamesRequestLogEventsAsSubscribed(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantPrefix string
		wantAbsent string
	}{
		{name: "request_log", query: "?types=request_log", wantPrefix: "request_log.", wantAbsent: "audit."},
		{name: "request_log with usage", query: "?types=request_log,usage", wantPrefix: "request_log.", wantAbsent: "audit."},
		{name: "request_log beside its alias", query: "?types=audit,request_log,usage", wantPrefix: "request_log.", wantAbsent: "audit."},
		{name: "audit alias", query: "?types=audit", wantPrefix: "audit.", wantAbsent: "request_log."},
		{name: "audit alias with usage", query: "?types=audit,usage", wantPrefix: "audit.", wantAbsent: "request_log."},
		{name: "no types", query: "", wantPrefix: "audit.", wantAbsent: "request_log."},
	}
	publish := func(broker *live.Broker) {
		broker.PublishRequestLogEvent(live.EventRequestLogStarted, &requestlog.LogEntry{
			ID:        "request-log-1",
			RequestID: "req-1",
			Timestamp: time.Now(),
			Method:    http.MethodPost,
		})
		broker.PublishRequestLogEvent(live.EventRequestLogUpdated, &requestlog.LogEntry{
			ID:        "request-log-1",
			RequestID: "req-1",
			Timestamp: time.Now(),
			// A captured body holding the member the legacy names rewrite.
			Data: &requestlog.LogData{RequestBody: map[string]any{"_live_state": "request_log.updated"}},
		})
	}
	assertNames := func(t *testing.T, body, wantPrefix, wantAbsent string) {
		t.Helper()
		require.Contains(t, body, "event: "+wantPrefix+"updated\n")
		require.Contains(t, body, `"type":"`+wantPrefix+`updated"`)
		require.Contains(t, body, `,"_live_state":"`+wantPrefix+`updated","_live_pending":true}`)
		require.NotContains(t, body, "event: "+wantAbsent)
		require.NotContains(t, body, `"type":"`+wantAbsent)
		require.NotContains(t, body, `,"_live_state":"`+wantAbsent)
		// The body is the request's own data, whatever the subscriber's names.
		require.Contains(t, body, `"request_body":{"_live_state":"request_log.updated"}`)
	}
	for _, tt := range tests {
		t.Run(tt.name+"/snapshot", func(t *testing.T) {
			broker := live.NewBroker(live.Config{Enabled: true})
			publish(broker)
			body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs"+tt.query)
			// A snapshot of the in-flight request: bodies are stripped.
			require.Contains(t, body, "event: "+tt.wantPrefix+"updated\n")
			require.Contains(t, body, `"_live_state":"`+tt.wantPrefix+`updated"`)
			require.NotContains(t, body, tt.wantAbsent)
		})
		t.Run(tt.name+"/replay after cursor", func(t *testing.T) {
			broker := live.NewBroker(live.Config{Enabled: true})
			publish(broker)
			query := "?cursor=1"
			if tt.query != "" {
				query = tt.query + "&cursor=1"
			}
			body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs"+query)
			require.Contains(t, body, "event: "+tt.wantPrefix+"updated\n")
			require.Contains(t, body, `"_live_state":"`+tt.wantPrefix+`updated"`)
			require.NotContains(t, body, tt.wantAbsent)
		})
		t.Run(tt.name+"/live", func(t *testing.T) {
			broker := live.NewBroker(live.Config{Enabled: true})
			h := NewHandler(nil, nil, WithLiveBroker(broker))
			ctx, cancel := context.WithCancel(context.Background())
			rec := newLiveSSERecorder()
			c, _ := echotest.Get(t, "/admin/live/logs"+tt.query,
				echotest.WithResponseWriter(func(http.ResponseWriter) http.ResponseWriter { return rec }))
			c.SetRequest(c.Request().WithContext(ctx))
			errCh := make(chan error, 1)
			go func() { errCh <- h.LiveLogs(c) }()

			waitForLiveOutput(t, rec, func(string) bool { return rec.statusCode() == http.StatusOK })
			publish(broker)
			waitForLiveOutput(t, rec, func(body string) bool { return strings.Contains(body, "updated\n") })
			cancel()
			require.NoError(t, <-errCh)
			assertNames(t, rec.bodyString(), tt.wantPrefix, tt.wantAbsent)
		})
	}
}

// The legacy names only rewrite the preview's own _live_state, never a body
// that happens to hold the same text.
func TestCutRequestLogLiveState(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		wantBefore string
		wantAfter  string
		wantCut    bool
	}{
		{
			name:       "preview state",
			data:       `{"id":"a","_live_state":"request_log.started","_live_pending":true}`,
			wantBefore: `{"id":"a",`,
			wantAfter:  `started","_live_pending":true}`,
			wantCut:    true,
		},
		{
			name:       "preview state after a body holding the same member",
			data:       `{"id":"a","data":{"request_body":{"_live_state":"request_log.x"}},"_live_state":"request_log.flushed","_remote_written_at":7}`,
			wantBefore: `{"id":"a","data":{"request_body":{"_live_state":"request_log.x"}},`,
			wantAfter:  `flushed","_remote_written_at":7}`,
			wantCut:    true,
		},
		{
			name: "only a body holds the member",
			data: `{"id":"a","data":{"request_body":{"_live_state":"request_log.x"}}}`,
		},
		{name: "no state", data: `{"id":"a"}`},
		{name: "usage state", data: `{"id":"a","_live_state":"usage.completed"}`},
		{name: "empty", data: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after := cutRequestLogLiveState([]byte(tt.data))
			if !tt.wantCut {
				assert.Equal(t, tt.data, string(before))
				assert.Nil(t, after)
				return
			}
			assert.Equal(t, tt.wantBefore, string(before))
			assert.Equal(t, tt.wantAfter, string(after))
		})
	}
}

func TestLiveLogsWritesResetAndReplayEvents(t *testing.T) {
	broker := live.NewBroker(live.Config{Enabled: true, BufferSize: 1, ReplayLimit: 1})
	broker.PublishRequestLogEvent(live.EventRequestLogStarted, &requestlog.LogEntry{
		ID:        "request-log-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Method:    http.MethodPost,
	})
	broker.PublishRequestLogEvent(live.EventRequestLogUpdated, &requestlog.LogEntry{
		ID:             "request-log-1",
		RequestID:      "req-1",
		Timestamp:      time.Now(),
		RequestedModel: "gpt-test",
	})
	broker.PublishRequestLogEvent(live.EventRequestLogUpdated, &requestlog.LogEntry{
		ID:        "request-log-1",
		RequestID: "req-1",
		Timestamp: time.Now(),
		Provider:  "openai",
	})

	body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=request_log&cursor=1")
	require.Contains(t, body, "event: reset")
	require.Contains(t, body, "event: request_log.updated")
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
				broker.PublishRequestLogEvent(live.EventRequestLogStarted, &requestlog.LogEntry{
					ID:        "request-log-1",
					RequestID: "req-1",
					Timestamp: time.Now(),
				})
			}

			body := runLiveLogsWithCanceledContext(t, broker, "/admin/live/logs?types=request_log,usage")
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
		"id":   "request-log-1",
		"data": map[string]any{"request_headers": map[string]string{"User-Agent": "a<b>&\"c\"\n\u2028"}},
	})
	require.NoError(t, err)
	previewData := json.RawMessage(`{"id":"request-log-1","_live_state":"request_log.updated","_live_pending":true}`)
	legacyPreviewData := json.RawMessage(`{"id":"request-log-1","_live_state":"audit.updated","_live_pending":true}`)
	cases := []struct {
		name  string
		event live.Event
		// legacyNames writes the frame for a subscriber on the audit.* names;
		// legacy is then the event that frame must spell out.
		legacyNames bool
		legacy      *live.Event
	}{
		{name: "heartbeat without sequence", event: live.Event{Type: live.EventHeartbeat, Timestamp: ts}},
		{name: "request log event with state", event: live.Event{Seq: 8, Type: live.EventRequestLogUpdated, RequestID: "req-1", Timestamp: ts, Data: previewData}},
		{
			name:        "request log event under legacy names",
			event:       live.Event{Seq: 8, Type: live.EventRequestLogUpdated, RequestID: "req-1", Timestamp: ts, Data: previewData},
			legacyNames: true,
			legacy:      &live.Event{Seq: 8, Type: "audit.updated", RequestID: "req-1", Timestamp: ts, Data: legacyPreviewData},
		},
		{
			name:        "request log event without data under legacy names",
			event:       live.Event{Seq: 9, Type: live.EventRequestLogRemoved, RequestID: "req-1", Timestamp: ts},
			legacyNames: true,
			legacy:      &live.Event{Seq: 9, Type: "audit.removed", RequestID: "req-1", Timestamp: ts},
		},
		{name: "usage event under legacy names", event: live.Event{Seq: 1, Type: live.EventUsageCompleted, Timestamp: ts, Data: json.RawMessage(`{"_live_state":"usage.completed"}`)}, legacyNames: true},
		{name: "heartbeat under legacy names", event: live.Event{Seq: 42, Type: live.EventHeartbeat, Timestamp: ts}, legacyNames: true},
		{name: "heartbeat with sequence", event: live.Event{Seq: 42, Type: live.EventHeartbeat, Timestamp: ts}},
		{name: "request log event", event: live.Event{Seq: 7, Type: live.EventRequestLogUpdated, RequestID: "req-1", Timestamp: ts, Data: data}},
		{name: "whole-second timestamp", event: live.Event{Seq: 1, Type: live.EventUsageCompleted, Timestamp: ts.Truncate(time.Second), Data: json.RawMessage(`{}`)}},
		{name: "non-UTC timestamp", event: live.Event{Seq: 1, Type: live.EventReset, Timestamp: ts.In(time.FixedZone("CEST", 2*60*60))}},
		{name: "request ID needing escapes", event: live.Event{Seq: 3, Type: live.EventRequestLogStarted, RequestID: "a\"b\\c<d>&e\n\x01", Timestamp: ts}},
		{name: "non-ASCII request ID", event: live.Event{Seq: 3, Type: live.EventRequestLogStarted, RequestID: "żółw-\u2028-\xff", Timestamp: ts}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.event
			if tc.legacy != nil {
				wire = *tc.legacy
			}
			payload, err := json.Marshal(wire)
			require.NoError(t, err)
			want := fmt.Sprintf("event: %s\ndata: %s\n\n", wire.Type, payload)
			if wire.Seq > 0 {
				want = fmt.Sprintf("id: %d\n", wire.Seq) + want
			}

			got, err := appendLiveEventFrame([]byte("previous frame"), tc.event, tc.legacyNames)
			require.NoError(t, err)
			assert.Equal(t, "previous frame"+want, string(got))
		})
	}
}

func TestLiveEventWriterDropsOversizedFrameBuffer(t *testing.T) {
	ts := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	w := liveEventWriter{res: rec}

	require.NoError(t, w.write(live.Event{Seq: 1, Type: live.EventHeartbeat, Timestamp: ts}))
	require.NotNil(t, w.buf, "small frame buffer is kept for reuse")
	assert.LessOrEqual(t, cap(w.buf), maxReusedLiveFrameBuffer)

	body := `"` + strings.Repeat("x", 2*maxReusedLiveFrameBuffer) + `"`
	large := live.Event{Seq: 2, Type: live.EventRequestLogUpdated, Timestamp: ts, Data: json.RawMessage(body)}
	require.NoError(t, w.write(large))
	assert.Nil(t, w.buf, "oversized frame buffer is dropped after writing")

	require.NoError(t, w.write(live.Event{Seq: 3, Type: live.EventHeartbeat, Timestamp: ts}))
	require.NotNil(t, w.buf)
	assert.LessOrEqual(t, cap(w.buf), maxReusedLiveFrameBuffer)

	written := rec.Body.String()
	assert.Contains(t, written, body, "large frame is written in full")
	assert.Equal(t, 3, strings.Count(written, "\n\n"), "every frame is written")
}

func BenchmarkAppendLiveEventFrame(b *testing.B) {
	event := live.Event{
		Seq:       12345,
		Type:      live.EventRequestLogUpdated,
		RequestID: "req-bench",
		Timestamp: time.Date(2026, 9, 15, 12, 0, 0, 123456789, time.UTC),
		Data:      json.RawMessage(`{"id":"request-log-bench","request_id":"req-bench","timestamp":"2026-09-15T12:00:00Z","requested_model":"gpt-4o-mini","provider":"openai","method":"POST","path":"/v1/chat/completions","data":{"user_agent":"openai-python/1.60.0","request_headers":{"Authorization":"[REDACTED]","Content-Type":"application/json"}},"_live_state":"request_log.updated","_live_pending":true}`),
	}
	for _, bench := range []struct {
		name        string
		legacyNames bool
	}{{name: "names", legacyNames: false}, {name: "legacy names", legacyNames: true}} {
		b.Run(bench.name, func(b *testing.B) {
			var buf []byte
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if buf, err = appendLiveEventFrame(buf[:0], event, bench.legacyNames); err != nil {
					b.Fatal(err)
				}
			}
		})
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
