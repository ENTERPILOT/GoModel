// Package alerts delivers gateway alerts to their destinations: the generic
// signed JSON webhook, and the alert sinks a distribution registers.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/observability"
)

const (
	// queueSize is how many alerts wait for one destination. An alert that
	// finds the queue full is dropped and counted as a delivery failure.
	queueSize = 256
	// sinkTimeout bounds one AlertSink.SendAlert call.
	sinkTimeout = 30 * time.Second
	// sinkReturnGrace is how long a sink has to return once its context has
	// ended, before the call is given up on.
	sinkReturnGrace = 2 * time.Second
	// drainTimeout is how long Close lets queued alerts finish. Alerts are
	// claimed before they are queued, so what is still undelivered after it
	// is lost: no replica sends it later.
	drainTimeout = 5 * time.Second
	// webhookDestination names every webhook in the delivery-failure metric,
	// so no sink may use it.
	webhookDestination = "webhook"
)

// WebhookConfig is one generic JSON webhook.
type WebhookConfig struct {
	URL string
	// Secret signs each delivery when set.
	Secret string
}

// Config lists where alerts go.
type Config struct {
	Webhooks []WebhookConfig
	Sinks    []ext.AlertSink
	// Timeout bounds one webhook attempt; MaxRetries is how often a failed
	// one is tried again.
	Timeout    time.Duration
	MaxRetries int
}

// destination is one place alerts are delivered to, with its own queue and
// worker so a slow or dead destination never delays another.
type destination struct {
	// metric labels the delivery-failure counter; label names the destination
	// in logs. Neither carries a URL path, query, or credential.
	metric string
	label  string
	send   func(ctx context.Context, event ext.AlertEvent) error
	queue  chan ext.AlertEvent
}

// Dispatcher delivers alerts asynchronously. Notify never blocks on a
// destination.
type Dispatcher struct {
	destinations []*destination

	mu     sync.RWMutex
	closed bool

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// New starts a dispatcher for the configured destinations. It returns nil
// when there are none, so callers can skip alert evaluation entirely.
func New(cfg Config) (*Dispatcher, error) {
	var destinations []*destination
	for i, hook := range cfg.Webhooks {
		webhook, err := newWebhook(hook, cfg.Timeout, cfg.MaxRetries)
		if err != nil {
			return nil, fmt.Errorf("budgets.alerts.webhooks[%d].url: %w", i, err)
		}
		destinations = append(destinations, &destination{
			metric: webhookDestination,
			label:  "webhook " + webhook.host,
			send:   webhook.send,
		})
	}
	names := make(map[string]bool, len(cfg.Sinks))
	for _, sink := range cfg.Sinks {
		name := sink.Name()
		// The name labels the sink's metric series, so it has to tell the sink
		// from the webhook and from every other sink.
		if name == "" || name == webhookDestination || names[name] {
			return nil, fmt.Errorf("alert sink name %q is empty, reserved, or registered twice", name)
		}
		names[name] = true
		destinations = append(destinations, &destination{
			metric: name,
			label:  "sink " + name,
			send:   sinkSender(sink),
		})
	}
	if len(destinations) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{destinations: destinations, ctx: ctx, cancel: cancel}
	for _, dest := range destinations {
		dest.queue = make(chan ext.AlertEvent, queueSize)
		d.wg.Go(func() { d.deliver(dest) })
	}
	return d, nil
}

// Notify queues event for every destination and returns at once.
func (d *Dispatcher) Notify(event ext.AlertEvent) {
	if d == nil {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	observability.BudgetAlertsFired.WithLabelValues(event.Budget.Scope, strconv.Itoa(event.ThresholdPercent)).Inc()
	for _, dest := range d.destinations {
		select {
		case dest.queue <- event:
		default:
			observability.BudgetAlertDeliveryFailures.WithLabelValues(dest.metric).Inc()
			slog.Warn("alert dropped: destination queue is full", "destination", dest.label, "alert_id", event.ID)
		}
	}
}

func (d *Dispatcher) deliver(dest *destination) {
	for event := range dest.queue {
		if d.ctx.Err() != nil {
			observability.BudgetAlertDeliveryFailures.WithLabelValues(dest.metric).Inc()
			slog.Warn("alert abandoned: the gateway stopped before it was delivered", "destination", dest.label,
				"alert_id", event.ID, "alert_type", event.Type)
			continue
		}
		if err := dest.send(d.ctx, event); err != nil {
			observability.BudgetAlertDeliveryFailures.WithLabelValues(dest.metric).Inc()
			slog.Warn("alert delivery failed", "destination", dest.label, "alert_id", event.ID,
				"alert_type", event.Type, "error", err)
			continue
		}
		slog.Debug("alert delivered", "destination", dest.label, "alert_id", event.ID, "alert_type", event.Type)
	}
}

// Close stops accepting alerts and gives queued ones drainTimeout to be
// delivered. It then cancels the deliveries in flight and abandons the rest of
// every queue; each alert it gives up on is logged and counted as a delivery
// failure. Those alerts were already claimed, so delivery is at most once. It
// is safe to call repeatedly.
//
// Close always returns: a webhook attempt ends with its context, and a sink
// that does not is given up on after sinkReturnGrace, so Close takes at most
// drainTimeout plus sinkReturnGrace.
func (d *Dispatcher) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		for _, dest := range d.destinations {
			close(dest.queue)
		}
		d.mu.Unlock()

		done := make(chan struct{})
		go func() {
			d.wg.Wait()
			close(done)
		}()
		timer := time.NewTimer(drainTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			d.cancel()
			<-done
		}
		d.cancel()
	})
	return nil
}

// sinkSender calls a sink with a deadline, one call at a time. A panicking
// sink is reported as a failed delivery, never allowed to take the gateway
// down.
//
// A sink has to return once its context ends. One that does not would hold
// its worker, and with it Close, for as long as it likes, so the call runs on
// its own goroutine and is given up on sinkReturnGrace after the context
// ended. Until that call returns the sink is not called again: every later
// alert fails at once.
func sinkSender(sink ext.AlertSink) func(context.Context, ext.AlertEvent) error {
	// running holds a token while a SendAlert call has not returned.
	running := make(chan struct{}, 1)
	return func(ctx context.Context, event ext.AlertEvent) error {
		select {
		case running <- struct{}{}:
		default:
			return errors.New("alert sink has not returned from an earlier alert")
		}
		ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			err := callSink(ctx, sink, event)
			<-running
			result <- err
		}()
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
		}
		grace := time.NewTimer(sinkReturnGrace)
		defer grace.Stop()
		select {
		case err := <-result:
			return err
		case <-grace.C:
			return fmt.Errorf("alert sink did not return within %s of its context ending (%w)", sinkReturnGrace, context.Cause(ctx))
		}
	}
}

func callSink(ctx context.Context, sink ext.AlertSink, event ext.AlertEvent) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("alert sink panicked: %v", recovered)
		}
	}()
	return sink.SendAlert(ctx, event)
}
