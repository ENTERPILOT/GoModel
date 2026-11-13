package live

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
)

// encoded returns a retained event as a subscriber replaying it receives it.
func encoded(t *testing.T, event retainedEvent) Event {
	t.Helper()
	events := encodeRetainedEvents([]retainedEvent{event})
	require.Len(t, events, 1, "retained event does not encode")
	return events[0]
}

// benchAuditEntry is one in-flight request with captured headers, shaped like
// what the audit middleware publishes on audit.updated.
func benchAuditEntry(requestID string) *auditlog.LogEntry {
	return &auditlog.LogEntry{
		ID:             "audit-" + requestID,
		RequestID:      requestID,
		Timestamp:      time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
		Method:         "POST",
		Path:           "/v1/chat/completions",
		RequestedModel: "gpt-4o-mini",
		Provider:       "openai",
		Data: &auditlog.LogData{
			UserAgent:      "openai-python/1.60.0",
			RequestHeaders: map[string]string{"Authorization": "[REDACTED]", "Content-Type": "application/json", "User-Agent": "openai-python/1.60.0"},
		},
	}
}

// encodeLifecycle publishes a varied request lifecycle: headers, a failover
// trail, guardrails, bodies, a terminal event, and an event too large to keep
// unencoded.
func encodeLifecycle(b *Broker) {
	entry := benchAuditEntry("req-1")
	b.PublishAuditEvent(EventAuditStarted, entry)
	entry.ResolvedModel = "openai/gpt-4o-mini"
	entry.Data.Attempts = []auditlog.AttemptSnapshot{{Seq: 1, Kind: "primary", Model: "gpt-4o-mini", StartedAt: entry.Timestamp, ErrorMessage: "upstream <timeout>"}}
	entry.Data.Guardrails = []auditlog.GuardrailOutcomeSnapshot{{Seq: 1, Phase: "prompt", Instance: "pii", Action: "allow", Detail: map[string]any{"x": 1}}}
	b.PublishAuditEvent(EventAuditUpdated, entry)
	entry.StatusCode, entry.DurationNs = 200, 812_000_000
	entry.Data.RequestBody = auditlog.CaptureLoggedBody([]byte(`{"model":"gpt-4o-mini"}`))
	entry.Data.ResponseHeaders = map[string]string{"X-Request-Id": "req-1"}
	b.PublishAuditEvent(EventAuditCompleted, entry)

	big := benchAuditEntry("req-2")
	big.Data.RequestHeaders["X-Big"] = strings.Repeat("<", maxRetainedEventBytes/4)
	b.PublishAuditEvent(EventAuditUpdated, big)

	b.PublishAuditEvent(EventAuditFlushed, entry)
	b.PublishAuditEvent(EventAuditUpdated, benchAuditEntry("req-3"))
}

// A dashboard connecting late replays exactly the bytes one connected all
// along received, although only the second made the broker encode at publish.
func TestBrokerReplayMatchesLiveEncoding(t *testing.T) {
	watched := NewBroker(Config{Enabled: true})
	early := watched.Subscribe(0)
	require.NotNil(t, early)
	defer early.Close()
	idle := NewBroker(Config{Enabled: true})

	encodeLifecycle(watched)
	encodeLifecycle(idle)

	unencoded := 0
	for _, event := range idle.events {
		if event.preview != nil {
			unencoded++
		}
	}
	require.Positive(t, unencoded, "the idle broker should keep previews unencoded")
	lateWatched := watched.Subscribe(0)
	require.NotNil(t, lateWatched)
	defer lateWatched.Close()
	late := idle.Subscribe(0)
	require.NotNil(t, late)
	defer late.Close()

	require.Len(t, idle.events, len(watched.events))
	for i := range idle.events {
		received := <-early.Events
		got := string(encoded(t, idle.events[i]).Data)
		assert.Equal(t, string(encoded(t, watched.events[i]).Data), got, "retained event %d", i)
		// Copies that carried bodies or went past the cap differ by design.
		if !strings.Contains(string(received.Data), `"request_body"`) && len(received.Data) <= maxRetainedEventBytes {
			assert.Equal(t, string(received.Data), got, "event %d as a connected dashboard received it", i)
		}
	}
	require.Len(t, late.Replay, len(lateWatched.Replay))
	for i := range late.Replay {
		assert.Equal(t, lateWatched.Replay[i], late.Replay[i], "active snapshot %d", i)
	}
}

// A cursor inside the ring replays encoded events, identical to the ones
// a dashboard connected at publish time received.
func TestBrokerReplayAfterCursorEncodesRetainedEvents(t *testing.T) {
	watched := NewBroker(Config{Enabled: true})
	early := watched.Subscribe(0)
	require.NotNil(t, early)
	defer early.Close()
	idle := NewBroker(Config{Enabled: true})

	for i := range 5 {
		entry := benchAuditEntry("req-" + strconv.Itoa(i))
		watched.PublishAuditEvent(EventAuditUpdated, entry)
		idle.PublishAuditEvent(EventAuditUpdated, entry)
	}

	late := idle.Subscribe(1)
	require.NotNil(t, late)
	defer late.Close()
	require.False(t, late.Reset)
	require.Len(t, late.Replay, 4)
	<-early.Events
	for i, event := range late.Replay {
		received := <-early.Events
		assert.Equal(t, received, event, "replay %d", i)
	}
}

// With no dashboard connected the broker encodes nothing: every event that
// fits the cap is retained as its preview.
func TestBrokerDoesNotEncodeWithoutSubscribers(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	entry := benchAuditEntry("req-1")
	b.PublishAuditEvent(EventAuditStarted, entry)
	b.PublishAuditEvent(EventAuditUpdated, entry)
	b.PublishAuditEvent(EventAuditFlushed, entry)
	for i, event := range b.events {
		assert.Nil(t, event.Data, "event %d was encoded at publish", i)
		assert.NotNil(t, event.preview, "event %d", i)
	}

	idle := testing.AllocsPerRun(100, func() { b.PublishAuditEvent(EventAuditUpdated, entry) })
	sub := b.Subscribe(0)
	require.NotNil(t, sub)
	defer sub.Close()
	go func() {
		for range sub.Events {
		}
	}()
	watched := testing.AllocsPerRun(100, func() { b.PublishAuditEvent(EventAuditUpdated, entry) })
	assert.Less(t, idle, watched, "publishing with no subscriber should skip the encoding")
}

// Only previews whose encoding provably fits the cap stay unencoded, so the
// ring holds no more than it did when every event was encoded at publish.
func TestBrokerKeepsOnlyBoundedPreviewsUnencoded(t *testing.T) {
	cases := []struct {
		header    string
		unencoded bool
	}{
		{header: strings.Repeat("\x01", 16), unencoded: true},
		{header: strings.Repeat("\x01", maxRetainedEventBytes/8), unencoded: true},
		// Fits the cap encoded, but only escape-free text does: the bound
		// cannot tell, so it is encoded at publish as before.
		{header: strings.Repeat("x", maxRetainedEventBytes/4)},
		// Encodes past the cap, so it is compacted at publish as before.
		{header: strings.Repeat("\x01", maxRetainedEventBytes/2)},
	}
	b := NewBroker(Config{Enabled: true})
	for i, tc := range cases {
		entry := benchAuditEntry("req-" + strconv.Itoa(i))
		entry.Data.RequestHeaders["X-Big"] = tc.header
		b.PublishAuditEvent(EventAuditUpdated, entry)
	}

	require.Len(t, b.events, len(cases))
	for i, event := range b.events {
		assert.Equal(t, cases[i].unencoded, event.preview != nil, "event %d kept unencoded", i)
		if event.preview != nil {
			assert.LessOrEqual(t, retainedSizeBound(event.preview), maxRetainedEventBytes, "event %d", i)
		}
		assert.LessOrEqual(t, len(encoded(t, event).Data), maxRetainedEventBytes, "event %d", i)
	}
}

func TestRetainedSizeBoundRejectsUnencodablePreviews(t *testing.T) {
	nan := math.NaN()
	cases := map[string]auditPreview{
		"year past 9999":    {Timestamp: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		"NaN temperature":   {Data: &auditPreviewData{Temperature: &nan}},
		"request body":      {Data: &auditPreviewData{RequestBody: "x"}},
		"attempt timestamp": {Data: &auditPreviewData{Attempts: []auditlog.AttemptSnapshot{{StartedAt: time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)}}}},
		"guardrail detail":  {Data: &auditPreviewData{Guardrails: []auditlog.GuardrailOutcomeSnapshot{{Detail: "x"}}}},
	}
	for name, preview := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, math.MaxInt, retainedSizeBound(&preview))
		})
	}
}

// A retained preview outlives the request, so changing the entry's values
// after publishing must not reach what a later subscriber replays.
func TestBrokerRetainedPreviewDoesNotAliasEntryValues(t *testing.T) {
	b := NewBroker(Config{Enabled: true})
	temperature, maxTokens := 0.2, 256
	entry := benchAuditEntry("req-1")
	entry.Data.Temperature, entry.Data.MaxTokens = &temperature, &maxTokens
	entry.Data.ResponseHeaders = map[string]string{"X-Request-Id": "req-1"}
	b.PublishAuditEvent(EventAuditCompleted, entry)
	b.PublishAuditEvent(EventAuditFlushed, entry)

	temperature, maxTokens = 1.5, 9
	entry.Data.RequestHeaders["Authorization"] = "sk-leaked"
	entry.Data.ResponseHeaders["X-Added-Later"] = "1"

	for i, event := range b.events {
		data, ok := eventPayload(t, encoded(t, event))["data"].(map[string]any)
		require.True(t, ok, "event %d", i)
		assert.InDelta(t, 0.2, data["temperature"], 1e-9, "event %d", i)
		assert.InDelta(t, 256, data["max_tokens"], 1e-9, "event %d", i)
		assert.Equal(t, map[string]any{"Authorization": "[REDACTED]", "Content-Type": "application/json", "User-Agent": "openai-python/1.60.0"}, data["request_headers"], "event %d", i)
		assert.Equal(t, map[string]any{"X-Request-Id": "req-1"}, data["response_headers"], "event %d", i)
	}
}

// The bound must cover every member a preview encodes, including ones added
// after it was written: fill every field reflectively with worst-case values
// and check the encoding stays within it.
func TestRetainedSizeBoundCoversEncoding(t *testing.T) {
	for _, text := range []string{"", "a", strings.Repeat("<\x01\xff\u2028", 40)} {
		preview := auditPreview{}
		fillWorstCase(reflect.ValueOf(&preview).Elem(), text)
		// Previews never carry attempt bodies or headers (compactAttemptsForPreview
		// drops them), and the bound refuses any that do.
		for i := range preview.Data.Attempts {
			preview.Data.Attempts[i].ResponseBody = nil
			preview.Data.Attempts[i].ResponseHeaders = nil
		}
		encoded, err := json.Marshal(&preview)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(encoded), retainedSizeBound(&preview), "text %q", text)
	}
}

// fillWorstCase sets every settable field to its longest encoding: text to
// text, numbers to their widest values, three elements per slice and map.
// Interface fields stay nil; the bound refuses previews that set them.
func fillWorstCase(v reflect.Value, text string) {
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -(12*3600+59*60)))))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(text)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(math.MinInt64)
	case reflect.Int8, reflect.Int16, reflect.Int32:
		v.SetInt(-1 << (v.Type().Bits() - 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(math.MaxUint64 >> (64 - v.Type().Bits()))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(-1.2345678901234567e-300)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillWorstCase(v.Elem(), text)
	case reflect.Struct:
		for _, field := range v.Fields() {
			if field.CanSet() {
				fillWorstCase(field, text)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 3, 3))
		for i := range 3 {
			fillWorstCase(v.Index(i), text)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		for i := range 3 {
			key := reflect.New(v.Type().Key()).Elem()
			fillWorstCase(key, text+strconv.Itoa(i))
			value := reflect.New(v.Type().Elem()).Elem()
			fillWorstCase(value, text)
			v.SetMapIndex(key, value)
		}
	}
}

// Publishes for the same requests race with dashboards connecting and
// replaying. Each subscriber must see one gap-free run of sequences across
// its replay and its live events, and retained previews must never change
// under a replay that is encoding them (the race detector checks that).
func TestBrokerConcurrentPublishAndReplay(t *testing.T) {
	b := NewBroker(Config{Enabled: true, SubscriberBuffer: 1 << 16})
	const publishers, perPublisher = 4, 300

	var wg sync.WaitGroup
	for p := range publishers {
		wg.Go(func() {
			for i := range perPublisher {
				entry := benchAuditEntry("req-" + strconv.Itoa(i%3))
				entry.Data.RequestHeaders = map[string]string{"X-Publisher": strconv.Itoa(p)}
				entry.StatusCode = i
				eventType := EventAuditUpdated
				if i%10 == 9 {
					eventType = EventAuditFlushed
				}
				b.PublishAuditEvent(eventType, entry)
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 20 {
				sub := b.Subscribe(1)
				if !assert.NotNil(t, sub) {
					return
				}
				next := sub.Latest + 1
				if !sub.Reset && len(sub.Replay) > 0 {
					assert.Equal(t, uint64(2), sub.Replay[0].Seq)
					for i, event := range sub.Replay {
						assert.Equal(t, uint64(2+i), event.Seq)
						assert.NotEmpty(t, event.Data)
					}
					assert.Equal(t, sub.Latest, sub.Replay[len(sub.Replay)-1].Seq)
				}
				for range 10 {
					select {
					case event := <-sub.Events:
						assert.Equal(t, next, event.Seq)
						assert.NotEmpty(t, event.Data)
						next++
					case <-time.After(time.Millisecond):
					}
				}
				sub.Close()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, uint64(publishers*perPublisher), b.LatestSeq())
}

// BenchmarkBrokerAuditPublishParallel publishes audit events for many
// concurrent requests with no dashboard connected: the broker mutex is the one
// gateway-wide serialization point on the request path, so how long each
// publish holds it decides how this scales with cores.
func BenchmarkBrokerAuditPublishParallel(b *testing.B) {
	broker := NewBroker(Config{Enabled: true})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		entries := make([]*auditlog.LogEntry, 16)
		for i := range entries {
			entries[i] = benchAuditEntry("req-" + strconv.Itoa(i))
		}
		i := 0
		for pb.Next() {
			broker.PublishAuditEvent(EventAuditUpdated, entries[i%len(entries)])
			i++
		}
	})
}

// BenchmarkBrokerSubscribeFullRing measures what a dashboard connecting with
// a cursor at the start of a full replay window costs.
func BenchmarkBrokerSubscribeFullRing(b *testing.B) {
	broker := NewBroker(Config{Enabled: true})
	for i := range defaultReplayLimit + 1 {
		broker.PublishAuditEvent(EventAuditUpdated, benchAuditEntry("req-"+strconv.Itoa(i)))
	}
	cursor := broker.LatestSeq() - defaultReplayLimit
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sub := broker.Subscribe(cursor)
		if sub == nil || len(sub.Replay) != defaultReplayLimit {
			b.Fatal("unexpected replay")
		}
		sub.Close()
	}
}
