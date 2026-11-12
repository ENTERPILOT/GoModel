// Package alerts delivers gateway alerts to their destinations: the generic
// signed JSON webhook, and the alert sinks a distribution registers.
package alerts

import (
	"context"
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
	// drainTimeout is how long Close lets queued alerts finish.
	drainTimeout = 5 * time.Second
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
			metric: "webhook",
			label:  "webhook " + webhook.host,
			send:   webhook.send,
		})
	}
	for _, sink := range cfg.Sinks {
		destinations = append(destinations, &destination{
			metric: sink.Name(),
			label:  "sink " + sink.Name(),
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
		if err := dest.send(d.ctx, event); err != nil {
			observability.BudgetAlertDeliveryFailures.WithLabelValues(dest.metric).Inc()
			slog.Warn("alert delivery failed", "destination", dest.label, "alert_id", event.ID,
				"alert_type", event.Type, "error", err)
			continue
		}
		slog.Debug("alert delivered", "destination", dest.label, "alert_id", event.ID, "alert_type", event.Type)
	}
}

// Close stops accepting alerts, gives queued ones a short time to be
// delivered, then abandons the rest. It is safe to call repeatedly.
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

// sinkSender calls a sink with a deadline. A panicking sink is reported as a
// failed delivery, never allowed to take the gateway down.
func sinkSender(sink ext.AlertSink) func(context.Context, ext.AlertEvent) error {
	return func(ctx context.Context, event ext.AlertEvent) (err error) {
		ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("alert sink panicked: %v", recovered)
			}
		}()
		return sink.SendAlert(ctx, event)
	}
}
