package lago

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

const testAPIKey = "lago-test-key-3f9a"

// testTiming flushes and retries within milliseconds.
var testTiming = timing{
	flush:    5 * time.Millisecond,
	request:  5 * time.Second,
	retryMin: time.Millisecond,
	retryMax: 4 * time.Millisecond,
	shutdown: 5 * time.Second,
}

// idle never flushes or retries by itself, so only Close sends.
var idleTiming = timing{
	flush:    time.Hour,
	request:  5 * time.Second,
	retryMin: time.Hour,
	retryMax: time.Hour,
	shutdown: 5 * time.Second,
}

func startExporter(t *testing.T, baseURL string, tm timing) *Exporter {
	t.Helper()
	exporter := newExporter(Options{
		APIURL:        baseURL + "/api/v1/",
		APIKey:        testAPIKey,
		Subscriptions: testSubscriptions,
		BufferSize:    1000,
	}, tm)
	t.Cleanup(exporter.Close)
	return exporter
}

// sentBatch decodes the events of one recorded request.
func sentBatch(t testing.TB, request providertest.Recorded) []Event {
	t.Helper()
	var body struct {
		Events []Event `json:"events"`
	}
	require.NoError(t, json.Unmarshal(request.Body, &body))
	return body.Events
}

// lagoServer remembers the transaction IDs of the events a fake Lago
// accepted, in arrival order.
type lagoServer struct {
	mu       sync.Mutex
	accepted []string
}

func (s *lagoServer) accept(w http.ResponseWriter, events []Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		s.accepted = append(s.accepted, event.TransactionID)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"events":[]}`)
}

func (s *lagoServer) acceptedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.accepted...)
}

// readBatch decodes the events of a request inside a fake Lago handler.
func readBatch(t *testing.T, r *http.Request) []Event {
	t.Helper()
	var body struct {
		Events []Event `json:"events"`
	}
	if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
		return nil
	}
	return body.Events
}

func unprocessable(w http.ResponseWriter, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = io.WriteString(w, `{"status":422,"error":"Unprocessable Entity","code":"validation_errors","error_details":`+details+`}`)
}

func TestExporterSendsEventsToBatchEndpoint(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"events":[]}`)
	exporter := startExporter(t, server.URL, testTiming)
	sent := testutil.ToFloat64(eventsSent)

	exporter.Record(testEntry("e1"))

	require.Eventually(t, func() bool { return capture.Count() == 1 }, 5*time.Second, time.Millisecond)
	request := capture.Last(t)
	assert.Equal(t, http.MethodPost, request.Method)
	assert.Equal(t, "/api/v1/events/batch", request.Path)
	assert.Equal(t, "Bearer "+testAPIKey, request.Header.Get("Authorization"))
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t,
		`{"events":[`+
			`{"transaction_id":"e1_tok_input","external_subscription_id":"sub_acme","code":"llm_input_tokens",`+
			`"timestamp":1800000000,"properties":{"value":1200,"model":"gpt-4o-mini","provider":"openai",`+
			`"provider_name":"openai-eu","user_path":"/customers/acme/app"}},`+
			`{"transaction_id":"e1_tok_output","external_subscription_id":"sub_acme","code":"llm_output_tokens",`+
			`"timestamp":1800000000,"properties":{"value":340,"model":"gpt-4o-mini","provider":"openai",`+
			`"provider_name":"openai-eu","user_path":"/customers/acme/app"}}]}`,
		string(request.Body))
	require.Eventually(t, func() bool { return testutil.ToFloat64(eventsSent)-sent == 2 }, 5*time.Second, time.Millisecond)
}

func TestExporterSkipsUnmappedRequests(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"events":[]}`)
	exporter := startExporter(t, server.URL, idleTiming)

	unmapped := testEntry("e1")
	unmapped.UserPath = "/customers/initech"
	exporter.Record(unmapped)
	exporter.Close()

	assert.Zero(t, capture.Count())
}

func TestExporterSendsAtMost100EventsPerRequest(t *testing.T) {
	lago := &lagoServer{}
	var sizes []int
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		events := readBatch(t, r)
		lago.mu.Lock()
		sizes = append(sizes, len(events))
		lago.mu.Unlock()
		lago.accept(w, events)
	})
	exporter := startExporter(t, server.URL, idleTiming)

	// 125 requests of two events each.
	for i := range 125 {
		exporter.Record(testEntry("e" + strconv.Itoa(i)))
	}
	// Full batches leave without waiting for the flush interval.
	require.Eventually(t, func() bool { return len(lago.acceptedIDs()) == 200 }, 5*time.Second, time.Millisecond)
	exporter.Close()

	ids := lago.acceptedIDs()
	require.Len(t, ids, 250)
	unique := make(map[string]bool, len(ids))
	for _, id := range ids {
		unique[id] = true
	}
	assert.Len(t, unique, 250)
	lago.mu.Lock()
	defer lago.mu.Unlock()
	assert.Equal(t, []int{100, 100, 50}, sizes)
}

func TestExporterRetriesWithTheSameTransactionIDs(t *testing.T) {
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusTooManyRequests,
		http.StatusUnauthorized, // a rotated key is fixed outside the batch
		http.StatusNotFound,     // so is a mistyped API URL
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			lago := &lagoServer{}
			var attempts atomic.Int32
			server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
				if attempts.Add(1) <= 2 {
					w.WriteHeader(status)
					return
				}
				lago.accept(w, readBatch(t, r))
			})
			exporter := startExporter(t, server.URL, testTiming)

			exporter.Record(testEntry("e1"))

			require.Eventually(t, func() bool { return len(lago.acceptedIDs()) == 2 }, 5*time.Second, time.Millisecond)
			requests := capture.All()
			require.Len(t, requests, 3)
			assert.Equal(t, string(requests[0].Body), string(requests[1].Body))
			assert.Equal(t, string(requests[0].Body), string(requests[2].Body))
			assert.Equal(t, []string{"e1_tok_input", "e1_tok_output"}, lago.acceptedIDs())
		})
	}
}

func TestExporterRetriesWhenLagoIsUnreachable(t *testing.T) {
	lago := &lagoServer{}
	var attempts atomic.Int32
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			// Drop the connection without an answer.
			hijacker, ok := w.(http.Hijacker)
			if !assert.True(t, ok) {
				return
			}
			conn, _, err := hijacker.Hijack()
			if assert.NoError(t, err) {
				_ = conn.Close()
			}
			return
		}
		lago.accept(w, readBatch(t, r))
	})
	exporter := startExporter(t, server.URL, testTiming)

	exporter.Record(testEntry("e1"))

	require.Eventually(t, func() bool { return len(lago.acceptedIDs()) == 2 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"e1_tok_input", "e1_tok_output"}, lago.acceptedIDs())
}

// Lago validates a batch as a whole: one bad event fails all of them, and
// error_details names the bad ones by index.
func TestExporterDropsNamedEventsAndSendsTheRest(t *testing.T) {
	lago := &lagoServer{}
	var attempts atomic.Int32
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		events := readBatch(t, r)
		if attempts.Add(1) == 1 {
			// Event 0 arrived with an earlier attempt whose answer was
			// lost; event 3 is refused for good.
			unprocessable(w, `{"0":{"transaction_id":["value_already_exist"]},"3":{"timestamp":["value_is_out_of_range"]}}`)
			return
		}
		lago.accept(w, events)
	})
	exporter := startExporter(t, server.URL, idleTiming)
	sent := testutil.ToFloat64(eventsSent)
	rejected := testutil.ToFloat64(eventsDropped.WithLabelValues(dropRejected))

	exporter.Record(testEntry("e1"))
	exporter.Record(testEntry("e2"))
	exporter.Close()

	require.Equal(t, 2, capture.Count())
	assert.Equal(t, []string{"e1_tok_output", "e2_tok_input"}, lago.acceptedIDs())
	assert.Equal(t, 3.0, testutil.ToFloat64(eventsSent)-sent, "two accepted and one Lago already held")
	assert.Equal(t, 1.0, testutil.ToFloat64(eventsDropped.WithLabelValues(dropRejected))-rejected)
}

// A refusal that names no event, such as a batch above a self-hosted
// instance's lowered limit, is narrowed down by halving.
func TestExporterHalvesABatchLagoRefusesWithoutNamingEvents(t *testing.T) {
	lago := &lagoServer{}
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		events := readBatch(t, r)
		if len(events) > 3 {
			unprocessable(w, `{"events":["too_many_events"]}`)
			return
		}
		for _, event := range events {
			if event.TransactionID == "e2_tok_output" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		lago.accept(w, events)
	})
	exporter := startExporter(t, server.URL, idleTiming)
	rejected := testutil.ToFloat64(eventsDropped.WithLabelValues(dropRejected))

	for i := range 5 {
		exporter.Record(testEntry("e" + strconv.Itoa(i)))
	}
	exporter.Close()

	assert.ElementsMatch(t, []string{
		"e0_tok_input", "e0_tok_output", "e1_tok_input", "e1_tok_output", "e2_tok_input",
		"e3_tok_input", "e3_tok_output", "e4_tok_input", "e4_tok_output",
	}, lago.acceptedIDs())
	assert.Equal(t, 1.0, testutil.ToFloat64(eventsDropped.WithLabelValues(dropRejected))-rejected)
}

// A batch Lago keeps refusing without naming events is halved into up to 199
// POSTs. Close stops that splitting and leaves the rest to the shutdown
// attempt, so its timeout bounds how long Close takes.
func TestExporterCloseStopsSplittingARefusedBatch(t *testing.T) {
	firstCh := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(firstCh) })
	var requests atomic.Int32
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		readBatch(t, r)
		if requests.Add(1) == 1 {
			<-firstCh
		}
		select { // a slow answer, so splitting all the way would take seconds
		case <-r.Context().Done():
		case <-time.After(20 * time.Millisecond):
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	})
	short := idleTiming
	short.shutdown = 100 * time.Millisecond
	exporter := startExporter(t, server.URL, short)
	// Cleanups run last-registered first: release the handler before the
	// exporter and server cleanups wait for it.
	t.Cleanup(releaseFirst)

	for i := range 50 { // 100 events: one full batch, sent at once
		exporter.Record(testEntry("e" + strconv.Itoa(i)))
	}
	require.Eventually(t, func() bool { return requests.Load() == 1 }, 5*time.Second, time.Millisecond)

	closed := make(chan struct{})
	started := time.Now()
	go func() {
		exporter.Close()
		close(closed)
	}()
	require.Eventually(t, exporter.stopping, 5*time.Second, time.Millisecond)
	releaseFirst()

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		require.Fail(t, "Close kept splitting the refused batch")
	}
	assert.Less(t, time.Since(started), 2*time.Second)
}

func TestExporterCloseSendsWhatIsBuffered(t *testing.T) {
	lago := &lagoServer{}
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		lago.accept(w, readBatch(t, r))
	})
	exporter := startExporter(t, server.URL, idleTiming)

	exporter.Record(testEntry("e1"))
	exporter.Record(testEntry("e2"))
	require.Zero(t, capture.Count(), "nothing is sent before the flush interval")
	exporter.Close()
	exporter.Close() // safe to repeat

	assert.Equal(t, []string{"e1_tok_input", "e1_tok_output", "e2_tok_input", "e2_tok_output"}, lago.acceptedIDs())
	require.Len(t, sentBatch(t, capture.Last(t)), 4)
}

func TestExporterCloseDuringARetryWaitCountsWhatIsLost(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusServiceUnavailable, `{}`)
	waiting := testTiming
	waiting.retryMin, waiting.retryMax = time.Hour, time.Hour
	exporter := startExporter(t, server.URL, waiting)
	lost := testutil.ToFloat64(eventsDropped.WithLabelValues(dropShutdown))

	exporter.Record(testEntry("e1"))
	require.Eventually(t, func() bool { return capture.Count() == 1 }, 5*time.Second, time.Millisecond)
	exporter.Close() // returns although the next attempt is an hour away

	assert.Equal(t, 2, capture.Count(), "Close makes one last attempt")
	assert.Equal(t, 2.0, testutil.ToFloat64(eventsDropped.WithLabelValues(dropShutdown))-lost)

	exporter.Record(testEntry("e2"))
	assert.Equal(t, 4.0, testutil.ToFloat64(eventsDropped.WithLabelValues(dropShutdown))-lost,
		"an entry recorded after Close is counted, not queued")
}

func TestExporterDropsNewRequestsWhenTheBufferIsFull(t *testing.T) {
	releaseCh := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseCh) })
	lago := &lagoServer{}
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		events := readBatch(t, r)
		<-releaseCh
		lago.accept(w, events)
	})
	exporter := newExporter(Options{
		APIURL:        server.URL,
		APIKey:        testAPIKey,
		Subscriptions: testSubscriptions,
		BufferSize:    1,
	}, testTiming)
	t.Cleanup(exporter.Close)
	// Registered last, so it runs first and a failed wait never hangs the
	// exporter and server cleanups.
	t.Cleanup(release)
	dropped := testutil.ToFloat64(eventsDropped.WithLabelValues(dropBufferFull))

	// The first request is in flight, the second waits in the buffer, and
	// the third has no room. Record returns at once every time.
	exporter.Record(testEntry("e1"))
	require.Eventually(t, func() bool { return capture.Count() == 1 }, 5*time.Second, time.Millisecond)
	exporter.Record(testEntry("e2"))
	exporter.Record(testEntry("e3"))

	assert.Equal(t, 2.0, testutil.ToFloat64(eventsDropped.WithLabelValues(dropBufferFull))-dropped)
	release()
	exporter.Close()
	assert.Equal(t, []string{"e1_tok_input", "e1_tok_output", "e2_tok_input", "e2_tok_output"}, lago.acceptedIDs())
}

func TestExporterNeverLogsTheAPIKey(t *testing.T) {
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	var attempts atomic.Int32
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		switch attempts.Add(1) {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"status":401,"error":"Unauthorized"}`)
		default:
			unprocessable(w, `{"0":{"code":["invalid"]},"1":{"code":["invalid"]}}`)
		}
	})
	exporter := startExporter(t, server.URL, testTiming)

	exporter.Record(testEntry("e1"))
	require.Eventually(t, func() bool { return capture.Count() == 2 }, 5*time.Second, time.Millisecond)
	exporter.Close()

	output := logs.String()
	assert.Contains(t, output, "lago export failed, retrying")
	assert.Contains(t, output, "lago rejected usage events")
	assert.NotContains(t, output, testAPIKey)
}

// syncBuffer collects log output written by the exporter goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
