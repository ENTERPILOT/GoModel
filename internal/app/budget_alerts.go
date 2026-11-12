package app

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/alerts"
	"github.com/enterpilot/gomodel/internal/budget"
)

// budgetAlerts is the soft-alert half of budgets for one configuration
// generation: the evaluator that finds budgets over a threshold and the
// dispatcher that delivers what it finds.
type budgetAlerts struct {
	alerter    *budget.Alerter
	dispatcher *alerts.Dispatcher

	mu   sync.Mutex
	stop func()
}

// initBudgetAlerts builds budget alerting when a destination exists: a
// configured webhook or an alert sink a distribution registered. Without one
// nothing is built, so nothing is ever evaluated.
func (b *bootstrap) initBudgetAlerts() error {
	app := b.app
	if app.budgets == nil || app.budgets.Service == nil {
		return nil
	}
	cfg := b.appCfg.Budgets.Alerts
	dispatcherCfg := alerts.Config{
		Timeout:    time.Duration(cfg.WebhookTimeout) * time.Second,
		MaxRetries: cfg.WebhookMaxRetries,
	}
	for _, hook := range cfg.Webhooks {
		dispatcherCfg.Webhooks = append(dispatcherCfg.Webhooks, alerts.WebhookConfig{URL: hook.URL, Secret: hook.Secret})
	}
	if b.cfg.Extensions != nil {
		for _, sink := range b.cfg.Extensions.AlertSinks() {
			if !nilInterface(sink) {
				dispatcherCfg.Sinks = append(dispatcherCfg.Sinks, sink)
			}
		}
	}

	dispatcher, err := alerts.New(dispatcherCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize budget alerts: %w", err)
	}
	if dispatcher == nil {
		return nil
	}
	alerter, err := budget.NewAlerter(app.budgets.Service, time.Duration(cfg.Interval)*time.Second, dispatcher.Notify)
	if err != nil {
		_ = dispatcher.Close()
		return fmt.Errorf("failed to initialize budget alerts: %w", err)
	}
	app.budgetAlerts = &budgetAlerts{alerter: alerter, dispatcher: dispatcher}
	app.register(subsystemBudgetAlerts, ownedByShutdown, app.budgetAlerts.Close)
	slog.Info("budget alerts enabled",
		"webhooks", len(dispatcherCfg.Webhooks),
		"sinks", len(dispatcherCfg.Sinks),
		"default_thresholds", cfg.Thresholds,
		"interval_seconds", cfg.Interval,
	)
	return nil
}

// start begins background evaluation. Like cluster sync it runs only for the
// generation that serves; a replacement still being built evaluates nothing.
func (a *budgetAlerts) start() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop == nil {
		a.stop = a.alerter.Start()
	}
}

// Close stops evaluating, then lets the dispatcher finish queued deliveries.
func (a *budgetAlerts) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	stop := a.stop
	a.stop = func() {} // a closed instance never starts
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	return a.dispatcher.Close()
}
