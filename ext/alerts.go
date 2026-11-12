package ext

import (
	"context"
	"time"
)

// AlertTypeBudgetThreshold is the AlertEvent.Type of a budget whose spend
// reached one of its alert thresholds.
const AlertTypeBudgetThreshold = "budget.threshold_reached"

// AlertEvent is one alert raised by the gateway. Its JSON form is the body of
// the generic alert webhook and is stable: fields are added, never renamed or
// removed.
type AlertEvent struct {
	// Type names the kind of alert, for example AlertTypeBudgetThreshold.
	Type string `json:"type"`
	// ID identifies the alert. It is derived from what fired, so a webhook
	// retry carries the same ID and a receiver can deduplicate on it.
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
type AlertSink interface {
	// Name identifies the sink in logs and metrics.
	Name() string
	SendAlert(ctx context.Context, event AlertEvent) error
}
