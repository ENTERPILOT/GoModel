package lago

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/usage"
)

// eventsSent counts usage events Lago holds.
var eventsSent = promauto.NewCounter(prometheus.CounterOpts{
	Name: "gomodel_lago_events_sent_total",
	Help: "Usage events Lago accepted, including events it already held from an attempt whose answer was lost",
})

// eventsDropped counts usage events that never reached Lago. Usage they carry
// is missing from the customer's invoice.
var eventsDropped = func() *prometheus.CounterVec {
	c := promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_lago_events_dropped_total",
			Help: "Usage events not delivered to Lago, by reason: buffer_full (LAGO_BUFFER_SIZE reached while Lago was slow or unreachable), rejected (Lago refused the event) or shutdown (still undelivered when the gateway stopped)",
		},
		[]string{"reason"},
	)
	// Export every reason from the start, so an increase() alert has a
	// baseline before the first drop.
	for _, reason := range []string{dropBufferFull, dropRejected, dropShutdown} {
		c.WithLabelValues(reason)
	}
	return c
}()

// Values of the reason label of eventsDropped.
const (
	dropBufferFull = "buffer_full"
	dropRejected   = "rejected"
	dropShutdown   = "shutdown"
)

// Options configures an Exporter.
type Options struct {
	// APIURL is the Lago API base URL, for example
	// "https://api.getlago.com/api/v1".
	APIURL string
	APIKey string
	// Subscriptions maps a canonical user path to the
	// external_subscription_id billed for it and everything below it.
	Subscriptions map[string]string
	// BufferSize is how many requests may wait to be sent.
	BufferSize int
}

// timing holds the exporter's intervals, so tests can shorten them.
type timing struct {
	// flush is how long a batch smaller than maxBatchSize waits for more.
	flush time.Duration
	// request bounds one POST.
	request time.Duration
	// retryMin and retryMax bound the wait between attempts, which doubles
	// after each failure.
	retryMin, retryMax time.Duration
	// shutdown bounds the last attempt for what is still unsent on Close,
	// after the request in flight has finished.
	shutdown time.Duration
}

var defaultTiming = timing{
	flush:    5 * time.Second,
	request:  10 * time.Second,
	retryMin: time.Second,
	retryMax: time.Minute,
	shutdown: 10 * time.Second,
}

// Exporter sends usage events to Lago from one background goroutine. Record
// never blocks: events wait in a bounded buffer and are dropped, counted and
// logged, when it is full.
type Exporter struct {
	client        *client
	subscriptions subscriptions
	timing        timing

	// queue holds the events of one request per slot, so a request is
	// buffered or dropped as a whole.
	queue chan []Event

	mu     sync.RWMutex // guards closed against a concurrent Record
	closed bool

	// done ends the waits of the background goroutine; stopped reports that
	// it returned.
	done    chan struct{}
	stopped chan struct{}
}

// New starts an exporter. Call Close to send what is buffered and stop it.
func New(opts Options) *Exporter {
	return newExporter(opts, defaultTiming)
}

func newExporter(opts Options, t timing) *Exporter {
	e := &Exporter{
		// The shared client carries the gateway's outbound TLS and proxy
		// settings and never follows a redirect to another origin, so the
		// API key stays with the configured host.
		client:        newClient(opts.APIURL, opts.APIKey, httpclient.NewClientWithTimeout(t.request)),
		subscriptions: opts.Subscriptions,
		timing:        t,
		queue:         make(chan []Event, max(opts.BufferSize, 1)),
		done:          make(chan struct{}),
		stopped:       make(chan struct{}),
	}
	go e.run()
	return e
}

// Record queues the events one usage entry is billed with. It runs on the
// request path and does not block.
func (e *Exporter) Record(entry *usage.UsageEntry) {
	events := e.subscriptions.events(entry)
	if len(events) == 0 {
		return
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		dropRequest(dropShutdown, "lago exporter closed, dropping usage events", entry, events)
		return
	}
	select {
	case e.queue <- events:
	default:
		dropRequest(dropBufferFull, "lago export buffer full, dropping usage events", entry, events)
	}
}

// dropRequest counts and logs the events of one request Record could not
// queue.
func dropRequest(reason, message string, entry *usage.UsageEntry, events []Event) {
	eventsDropped.WithLabelValues(reason).Add(float64(len(events)))
	slog.Warn(message,
		"request_id", cmp.Or(entry.RequestID, "unknown"),
		"subscription", events[0].ExternalSubscriptionID,
		"events", len(events))
}

// Close stops the exporter after one last attempt to send what is buffered.
// A request in flight finishes first; a wait between retries ends at once.
// Events Lago does not take within the shutdown timeout are counted as
// dropped. Close is safe to call more than once.
func (e *Exporter) Close() {
	e.mu.Lock()
	first := !e.closed
	e.closed = true
	e.mu.Unlock()

	if first {
		close(e.done)
	}
	<-e.stopped
}

// stopping reports whether Close was called.
func (e *Exporter) stopping() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// run batches queued events and sends them until Close.
func (e *Exporter) run() {
	defer close(e.stopped)

	ticker := time.NewTicker(e.timing.flush)
	defer ticker.Stop()

	var pending []Event
	for {
		all := false
		select {
		case events := <-e.queue:
			pending = append(pending, events...)
		case <-ticker.C:
			all = true
		case <-e.done:
			e.shutdown(pending)
			return
		}
		pending = e.flush(pending, all)
	}
}

// flush sends pending in full batches, and with all what is left over too.
// It returns the events still to send.
func (e *Exporter) flush(pending []Event, all bool) []Event {
	for !e.stopping() && (len(pending) >= maxBatchSize || (all && len(pending) > 0)) {
		n := min(len(pending), maxBatchSize)
		// Only Close leaves events unsent.
		if unsent := e.deliver(context.Background(), pending[:n:n], true); len(unsent) > 0 {
			return append(unsent, pending[n:]...)
		}
		pending = pending[n:]
	}
	return pending
}

// shutdown makes the last attempt for everything still unsent. Record takes
// nothing more by now, so draining the queue empties it for good.
func (e *Exporter) shutdown(pending []Event) {
	for drained := false; !drained; {
		select {
		case events := <-e.queue:
			pending = append(pending, events...)
		default:
			drained = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.timing.shutdown)
	defer cancel()
	lost := 0
	for len(pending) > 0 {
		n := min(len(pending), maxBatchSize)
		lost += len(e.deliver(ctx, pending[:n:n], false))
		pending = pending[n:]
	}
	if lost > 0 {
		eventsDropped.WithLabelValues(dropShutdown).Add(float64(lost))
		slog.Error("lago export: usage events not delivered before shutdown", "events", lost)
	}
}

// deliver sends one batch until Lago has settled every event in it: accepted,
// already held, or refused. A failure that does not come from the batch (Lago
// unreachable or failing, a rejected key, throttling) is waited out and the
// same events, with the same transaction IDs, are sent again. Without retry,
// or once Close is called, deliver returns the events it could not send
// instead.
func (e *Exporter) deliver(ctx context.Context, batch []Event, retry bool) (unsent []Event) {
	wait := e.timing.retryMin
	for len(batch) > 0 {
		status, body, err := e.send(ctx, batch)
		if err == nil && status >= http.StatusOK && status < http.StatusMultipleChoices {
			eventsSent.Add(float64(len(batch)))
			return nil
		}

		if err == nil && rejectsBatch(status) {
			rejected, duplicates := rejectedEvents(body, len(batch))
			switch {
			case len(rejected) > 0:
				// Lago refused the whole batch and named the events at
				// fault. An event it already holds is delivered; the others
				// named are lost; the rest is sent again without them.
				eventsSent.Add(float64(duplicates))
				logRejected(len(rejected)-duplicates, status, body)
				kept := make([]Event, 0, len(batch)-len(rejected))
				for i, event := range batch {
					if !rejected[i] {
						kept = append(kept, event)
					}
				}
				batch, wait = kept, e.timing.retryMin
			case len(batch) == 1:
				logRejected(1, status, body)
				return nil
			default:
				// Nothing names the events at fault: halve the batch until
				// they are alone.
				mid := len(batch) / 2
				unsent = e.deliver(ctx, batch[:mid:mid], retry)
				return append(unsent, e.deliver(ctx, batch[mid:], retry)...)
			}
			continue
		}

		if !retry || e.stopping() {
			return batch
		}
		failure := slog.Int("status", status)
		if err != nil {
			failure = slog.Any("error", err)
		}
		slog.Warn("lago export failed, retrying", failure, "events", len(batch), "retry_in", wait.String())
		select {
		case <-e.done:
			return batch
		case <-time.After(wait):
		}
		wait = min(wait*2, e.timing.retryMax)
	}
	return nil
}

func (e *Exporter) send(ctx context.Context, batch []Event) (status int, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, e.timing.request)
	defer cancel()
	return e.client.send(ctx, batch)
}

// logRejected counts and logs events Lago refused. The answer is Lago's
// validation error; it names fields and codes, never the API key.
func logRejected(events, status int, body []byte) {
	if events <= 0 {
		return
	}
	eventsDropped.WithLabelValues(dropRejected).Add(float64(events))
	const maxLogged = 512
	if len(body) > maxLogged {
		body = body[:maxLogged]
	}
	slog.Error("lago rejected usage events", "events", events, "status", status, "response", string(body))
}
