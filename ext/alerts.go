package ext

import (
	"context"
	"time"
)

// AlertTypeBudgetThreshold is the AlertEvent.Type of a budget whose spend
// reached one of its alert thresholds.
const AlertTypeBudgetThreshold = "budget.threshold_reached"

// AlertSchemaVersion is the AlertEvent.SchemaVersion core sends. It changes
// only if a field is renamed, removed, or changes meaning; added fields keep
// it.
const AlertSchemaVersion = 1

// AlertEvent is one alert raised by the gateway. Its JSON form is the body of
// the generic alert webhook and is stable: fields are added, never renamed or
// removed, within one SchemaVersion.
type AlertEvent struct {
	// SchemaVersion is the version of this format, AlertSchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// Type names the kind of alert, for example AlertTypeBudgetThreshold.
	Type string `json:"type"`
	// ID identifies the alert. Two deliveries carry the same ID exactly when
	// they are the same alert: for a budget threshold, the same budget scope,
	// subject, effective subject, and period, the same Budget.Revision, the
	// same period start, and the same threshold. Retries, and the instance of
	// another replica, send the same ID, so a receiver can deduplicate on it.
	// A new period, a reset, a moved period anchor, or a new budget revision
	// gives a new ID.
	ID string `json:"id"`
	// Timestamp is when the gateway raised the alert, in UTC.
	Timestamp time.Time `json:"timestamp"`
	// Budget is the budget the alert is about.
	Budget AlertBudget `json:"budget"`
	// ThresholdPercent is the threshold that was reached, as a percentage of
	// the budget amount.
	ThresholdPercent int `json:"threshold_percent"`
	// Spent is the tracked spend of the current period when the alert was
	// raised, on the budget's cost basis.
	Spent float64 `json:"spent"`
	// UsageRatio is Spent divided by the budget amount. It is not clamped:
	// a value above 1 means the budget is exceeded.
	UsageRatio float64 `json:"usage_ratio"`
	// PeriodStart and PeriodEnd bound the budget period the alert belongs to.
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	// ProjectedSpend is the spend the period ends on if spending continues at
	// the rate seen so far. It is nil when too little of the period has
	// elapsed for the projection to mean anything.
	ProjectedSpend *float64 `json:"projected_spend,omitempty"`
	// ProjectedExhaustionAt is when the budget runs out at that rate, in UTC.
	// It is nil without a projection, when the budget lasts the period, and
	// when it is already spent.
	ProjectedExhaustionAt *time.Time `json:"projected_exhaustion_at,omitempty"`
}

// AlertBudget identifies the budget of an AlertEvent.
type AlertBudget struct {
	// Scope is "user_path", "label", or "provider".
	Scope string `json:"scope"`
	// Subject is the budget's configured subject: a user path, a label, or a
	// provider name. For a per-child budget it is the template path.
	Subject string `json:"subject"`
	// EffectiveSubject is what the spend was counted for. It equals Subject
	// except for a per-child budget, where it is the child user path.
	EffectiveSubject string `json:"effective_subject"`
	// PerChild reports a per-child template budget.
	PerChild bool `json:"per_child"`
	// PeriodSeconds identifies the reset period; Period is its name
	// ("hourly", "daily", "weekly", "monthly", or "<n>s").
	PeriodSeconds int64  `json:"period_seconds"`
	Period        string `json:"period"`
	// Amount is the budget limit for the period.
	Amount float64 `json:"amount"`
	// CostBasis is the spend the budget is measured on: "provider" or "billed".
	CostBasis string `json:"cost_basis"`
	// Revision identifies the version of the budget the alert was raised
	// for. It is opaque and never used twice: the budget gets a new one when
	// its amount or cost basis changes, even back to an earlier value, and
	// when it is deleted and recreated. It stays the same when only its
	// thresholds are edited.
	Revision string `json:"revision"`
}

// AlertSink delivers gateway alerts to a channel core does not ship, such as
// a chat tool or email. Core delivers the same events to its generic JSON
// webhook.
//
// Delivery is at most once, and not guaranteed. An alert is recorded in the
// database before it is handed to any sink and is never raised again, so
// gateways sharing a database call SendAlert at most once for an alert, and
// a sink misses the alerts that:
//
//   - it returned an error for, panicked on, or did not return from in time.
//     Core does not retry: a sink that wants retries does them itself, within
//     the deadline.
//   - were raised while 256 alerts were already waiting for this sink. Each
//     sink has its own queue, so that takes a sink slower than alerts are
//     raised, and a slow sink delays no other destination.
//   - were still waiting or in flight when their gateway stopped. On
//     shutdown, and when a configuration reload replaces the gateway that
//     raised them, queued alerts have 5 seconds to be delivered; a process
//     that is killed loses them at once.
//
// Core logs every alert a sink missed and counts it in
// gomodel_budget_alert_delivery_failures_total under the sink's name, except
// those lost with a killed process. Gateways that do not share a database, as
// with SQLite, each raise the alert: a sink that must not repeat one
// deduplicates on AlertEvent.ID.
//
// SendAlert is called off the request path. One gateway calls a sink with one
// alert at a time, but a sink is registered once and outlives configuration
// reloads, and the gateway a reload replaced may still be delivering, so it
// must be safe for concurrent use.
//
// ctx ends after 30 seconds, or sooner when a stopping gateway gives up on
// its queue, and SendAlert must return once it has. When it has not returned
// 2 seconds later, core stops waiting for it: the alert counts as missed, and
// so does every later alert that gateway has for this sink, without a call,
// until SendAlert returns. A stopping gateway therefore waits at most 7
// seconds for its sinks.
//
// Like every registered component, a sink is owned by the distribution that
// registered it, not by core. It may implement SharedStoreAware to share
// state, such as its own deduplication, across replicas; the store stays open
// until core has stopped waiting for SendAlert, on shutdown too. For outbound
// calls it uses NewHTTPClient, so they follow the gateway's proxy environment
// and http.tls trust. Core starts no SendAlert call after the app's Shutdown
// has returned, and a call that returned when its context ended is over by
// then, so the distribution releases the sink's resources after that.
type AlertSink interface {
	// Name identifies the sink in logs and as the destination label of the
	// delivery-failure metric. It must not be empty, the name of another
	// registered sink, or "webhook", which is core's webhook: a gateway that
	// builds alerts with such a sink does not start.
	Name() string
	SendAlert(ctx context.Context, event AlertEvent) error
}
