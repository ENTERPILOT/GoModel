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
	// for. It is opaque; it changes when the amount changes or the budget is
	// deleted and recreated, and stays the same when only its thresholds are
	// edited.
	Revision string `json:"revision"`
}

// AlertSink delivers gateway alerts to a channel core does not ship, such as
// a chat tool or email. Core delivers the same events to its generic JSON
// webhook.
//
// SendAlert is called off the request path, one event at a time per sink, and
// ctx carries a deadline. An error is logged and counted, never fatal, and
// core does not retry it: a sink that wants retries does them itself within
// the deadline. A sink is registered once and outlives configuration
// reloads, so it must be safe for concurrent use.
//
// Like every registered component, a sink is owned by the distribution that
// registered it, not by core. It may implement SharedStoreAware to share
// state, such as its own deduplication, across replicas. For outbound calls
// it uses NewHTTPClient, so they follow the gateway's proxy environment and
// http.tls trust. Core stops calling SendAlert before the app's Shutdown
// returns, so the distribution releases the sink's resources after that.
type AlertSink interface {
	// Name identifies the sink in logs and metrics.
	Name() string
	SendAlert(ctx context.Context, event AlertEvent) error
}
