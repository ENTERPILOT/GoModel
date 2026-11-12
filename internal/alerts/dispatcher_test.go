package alerts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/observability"
)

// chanSink hands every alert to a channel, optionally failing afterwards.
type chanSink struct {
	name   string
	events chan ext.AlertEvent
	err    error
	panics bool
	// hold, when set, blocks SendAlert until it is closed.
	hold chan struct{}
}

func (s *chanSink) Name() string { return s.name }

func (s *chanSink) SendAlert(ctx context.Context, event ext.AlertEvent) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("sink called without a deadline")
	}
	s.events <- event
	if s.hold != nil {
		<-s.hold
	}
	if s.panics {
		panic("sink bug")
	}
	return s.err
}

func receive(t *testing.T, events <-chan ext.AlertEvent) ext.AlertEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no alert delivered")
		return ext.AlertEvent{}
	}
}

func TestNewWithoutDestinationsIsNil(t *testing.T) {
	dispatcher, err := New(Config{})
	require.NoError(t, err)
	require.Nil(t, dispatcher)

	// A nil dispatcher is inert.
	dispatcher.Notify(testEvent())
	require.NoError(t, dispatcher.Close())
}

func TestNewNamesTheInvalidWebhookWithoutItsURL(t *testing.T) {
	_, err := New(Config{Webhooks: []WebhookConfig{
		{URL: "https://hooks.example.com/ok"},
		{URL: "hooks.example.com/t0ken"},
	}})
	require.ErrorContains(t, err, "budgets.alerts.webhooks[1].url")
	assert.NotContains(t, err.Error(), "t0ken")
}

func TestDispatcherDeliversToEveryDestination(t *testing.T) {
	observability.ResetMetrics()
	hookEvents := make(chan ext.AlertEvent, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		hookEvents <- ext.AlertEvent{ID: r.Header.Get(HeaderDelivery)}
	}))
	defer server.Close()

	healthy := &chanSink{name: "chat", events: make(chan ext.AlertEvent, 4)}
	failing := &chanSink{name: "mail", events: make(chan ext.AlertEvent, 4), err: errors.New("smtp down")}
	panicking := &chanSink{name: "pager", events: make(chan ext.AlertEvent, 4), panics: true}

	dispatcher, err := New(Config{
		Webhooks: []WebhookConfig{{URL: server.URL}},
		Sinks:    []ext.AlertSink{failing, panicking, healthy},
	})
	require.NoError(t, err)
	require.NotNil(t, dispatcher)

	first, second := testEvent(), testEvent()
	second.ID = "bgt_second"
	dispatcher.Notify(first)
	dispatcher.Notify(second)

	// A failing or panicking sink neither stops the others nor its own next alert.
	for _, sink := range []*chanSink{healthy, failing, panicking} {
		assert.Equal(t, first, receive(t, sink.events), sink.name)
		assert.Equal(t, second, receive(t, sink.events), sink.name)
	}
	assert.Equal(t, "bgt_test", receive(t, hookEvents).ID)
	assert.Equal(t, "bgt_second", receive(t, hookEvents).ID)

	require.NoError(t, dispatcher.Close())
	require.NoError(t, dispatcher.Close(), "Close is idempotent")
	dispatcher.Notify(testEvent()) // dropped, must not panic

	assert.Equal(t, float64(2), testutil.ToFloat64(observability.BudgetAlertsFired.WithLabelValues("user_path", "80")))
	assert.Equal(t, float64(2), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("mail")))
	assert.Equal(t, float64(2), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("pager")))
	assert.Equal(t, float64(0), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("chat")))
	assert.Equal(t, float64(0), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("webhook")))
}

// Notify is called by the budget evaluator between budgets, so a destination
// that hangs must cost it nothing.
func TestDispatcherNotifyNeverBlocksOnAStuckDestination(t *testing.T) {
	observability.ResetMetrics()
	stuck := &chanSink{name: "stuck", events: make(chan ext.AlertEvent, 1), hold: make(chan struct{})}
	healthy := &chanSink{name: "healthy", events: make(chan ext.AlertEvent, queueSize+2)}

	dispatcher, err := New(Config{Sinks: []ext.AlertSink{stuck, healthy}})
	require.NoError(t, err)

	dispatcher.Notify(testEvent())
	receive(t, stuck.events) // the stuck sink now holds its worker

	// Fill the stuck queue, then one more: it is dropped, not waited for.
	for range queueSize + 1 {
		dispatcher.Notify(testEvent())
	}
	for range queueSize + 2 {
		receive(t, healthy.events)
	}
	assert.Equal(t, float64(1), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("stuck")))

	close(stuck.hold)
	go func() {
		for range stuck.events {
			// Drain so the worker can finish its queue.
		}
	}()
	require.NoError(t, dispatcher.Close())
	close(stuck.events)
}

// ctxSink blocks every alert until its context ends, like a sink whose
// endpoint never answers, and records how each call ended.
type ctxSink struct {
	name    string
	started chan string
	ended   chan error
}

func (s *ctxSink) Name() string { return s.name }

func (s *ctxSink) SendAlert(ctx context.Context, event ext.AlertEvent) error {
	s.started <- event.ID
	<-ctx.Done()
	s.ended <- ctx.Err()
	return ctx.Err()
}

func newCtxSink(name string) *ctxSink {
	return &ctxSink{name: name, started: make(chan string, queueSize), ended: make(chan error, queueSize)}
}

func eventWithID(id string) ext.AlertEvent {
	event := testEvent()
	event.ID = id
	return event
}

func TestDispatcherSurvivesMisbehavingSinks(t *testing.T) {
	tests := []struct {
		name string
		sink func() (ext.AlertSink, func(t *testing.T))
		// wait is how long each alert takes to fail.
		wait time.Duration
	}{
		{
			name: "panics",
			sink: func() (ext.AlertSink, func(t *testing.T)) {
				sink := &chanSink{name: "panics", events: make(chan ext.AlertEvent, 2), panics: true}
				return sink, func(t *testing.T) {
					assert.Equal(t, "first", (<-sink.events).ID)
					assert.Equal(t, "second", (<-sink.events).ID)
				}
			},
		},
		{
			name: "blocks past the sink timeout",
			sink: func() (ext.AlertSink, func(t *testing.T)) {
				sink := newCtxSink("blocks")
				return sink, func(t *testing.T) {
					assert.Equal(t, "first", <-sink.started)
					require.ErrorIs(t, <-sink.ended, context.DeadlineExceeded, "the sink timeout ended the call")
					assert.Equal(t, "second", <-sink.started)
					require.ErrorIs(t, <-sink.ended, context.DeadlineExceeded)
				}
			},
			wait: sinkTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				observability.ResetMetrics()
				sink, check := tt.sink()
				dispatcher, err := New(Config{Sinks: []ext.AlertSink{sink}})
				require.NoError(t, err)

				dispatcher.Notify(eventWithID("first"))
				dispatcher.Notify(eventWithID("second"))
				time.Sleep(2*tt.wait + time.Second)
				synctest.Wait()
				check(t)
				assert.Equal(t, float64(2), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues(sink.Name())),
					"the next alert is still delivered, and each failure is counted")
				require.NoError(t, dispatcher.Close())
			})
		})
	}
}

func TestDispatcherCloseWithAlertsQueued(t *testing.T) {
	t.Run("delivers what finishes within the drain timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			observability.ResetMetrics()
			sink := &slowSink{delay: time.Second}
			dispatcher, err := New(Config{Sinks: []ext.AlertSink{sink}})
			require.NoError(t, err)
			for _, id := range []string{"a", "b", "c"} {
				dispatcher.Notify(eventWithID(id))
			}
			start := time.Now()
			require.NoError(t, dispatcher.Close())
			assert.Equal(t, 3*time.Second, time.Since(start))
			assert.Equal(t, []string{"a", "b", "c"}, sink.delivered)
			assert.Zero(t, testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("slow")))
		})
	})
	t.Run("abandons and counts the rest after it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			observability.ResetMetrics()
			sink := newCtxSink("stuck")
			dispatcher, err := New(Config{Sinks: []ext.AlertSink{sink}})
			require.NoError(t, err)
			for _, id := range []string{"a", "b", "c"} {
				dispatcher.Notify(eventWithID(id))
			}
			synctest.Wait()
			start := time.Now()
			require.NoError(t, dispatcher.Close())
			assert.Equal(t, drainTimeout, time.Since(start), "Close waits the drain timeout, not the sink timeout")

			assert.Equal(t, "a", <-sink.started)
			require.ErrorIs(t, <-sink.ended, context.Canceled, "the delivery in flight is cancelled")
			assert.Empty(t, sink.started, "queued alerts are not handed to the sink after Close gives up")
			assert.Equal(t, float64(3), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("stuck")),
				"every abandoned alert is counted")

			dispatcher.Notify(eventWithID("late"))
			assert.Equal(t, float64(3), testutil.ToFloat64(observability.BudgetAlertDeliveryFailures.WithLabelValues("stuck")),
				"an alert after Close is not queued")
		})
	})
}

// slowSink takes delay for every alert and records the order it got them in.
type slowSink struct {
	delay     time.Duration
	delivered []string
}

func (*slowSink) Name() string { return "slow" }

func (s *slowSink) SendAlert(ctx context.Context, event ext.AlertEvent) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	s.delivered = append(s.delivered, event.ID)
	return nil
}
