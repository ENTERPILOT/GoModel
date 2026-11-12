package budget

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Soft alerts: each budget has alert thresholds, percentages of its amount.
// An Alerter evaluates them in the background and raises one event per
// budget revision, period, and threshold; see alerter.go.

// MinProjectionPeriodRatio is the share of a budget period that has to elapse
// before spend is projected to the period end. Earlier, a single request
// extrapolates to a wildly wrong total: with 5%, a monthly budget is projected
// from about a day and a half in, a daily one after 72 minutes.
const MinProjectionPeriodRatio = 0.05

// AlertSettings describes soft alerting for the deployment.
type AlertSettings struct {
	// Enabled reports that an alert destination exists, so thresholds are
	// evaluated.
	Enabled bool
	// DefaultThresholds apply to every budget without its own.
	DefaultThresholds []int
}

// WithAlertThresholds sets the alert thresholds of budgets without their own.
// An empty list keeps the built-in defaults.
func WithAlertThresholds(thresholds []int) ServiceOption {
	return func(service *Service) {
		if len(thresholds) > 0 {
			service.alertDefaults = slices.Clone(thresholds)
		}
	}
}

// AlertSettings returns whether alerts are evaluated and the default
// thresholds.
func (s *Service) AlertSettings() AlertSettings {
	if s == nil {
		return AlertSettings{}
	}
	return AlertSettings{
		Enabled:           s.alertsEnabled.Load(),
		DefaultThresholds: slices.Clone(s.alertDefaults),
	}
}

// alertThresholds returns the thresholds that apply to budget, ascending.
func (s *Service) alertThresholds(budget Budget) []int {
	if len(budget.AlertThresholds) > 0 {
		return budget.AlertThresholds
	}
	return s.alertDefaults
}

// Projection extrapolates a budget's spend linearly to the end of its period.
type Projection struct {
	// Spend is the spend the period ends on at the rate seen so far.
	Spend float64
	// ExhaustionAt is when the budget runs out at that rate. It is nil when
	// the budget lasts the period, or is already spent.
	ExhaustionAt *time.Time
}

// Projection extrapolates the spend seen so far to the end of the period. It
// reports false while less than MinProjectionPeriodRatio of the period has
// elapsed, and for a per-child template, which has no single spend.
func (r CheckResult) Projection(now time.Time) (Projection, bool) {
	if r.Budget.PerChild && r.Budget.EffectiveSubject == "" {
		return Projection{}, false
	}
	ratio := r.PeriodRatio(now)
	if ratio < MinProjectionPeriodRatio {
		return Projection{}, false
	}
	projection := Projection{Spend: r.Spent / ratio}
	if r.Spent > 0 && r.Spent < r.Budget.Amount && projection.Spend > r.Budget.Amount {
		elapsed := now.Sub(r.PeriodStart)
		at := r.PeriodStart.Add(time.Duration(float64(elapsed) * r.Budget.Amount / r.Spent)).UTC().Truncate(time.Second)
		projection.ExhaustionAt = &at
	}
	return projection, true
}

// thresholdReached reports whether the result's spend is at or past percent
// of the budget amount. A budget without priced usage never alerts.
func (r CheckResult) thresholdReached(percent int) bool {
	return r.HasUsage && r.Spent >= r.Budget.Amount*float64(percent)/100
}

// AlertClaim identifies one alert: a budget revision (with the child a
// per-child template was resolved to), the period it is in, and a threshold.
//
// Revision is part of the identity so that changing a budget's amount, or
// deleting and recreating it, re-arms its thresholds for the rest of the
// period: the alerts fired against the old amount say nothing about the new
// one. Editing only the thresholds is not a new revision: a threshold already
// claimed in the period stays claimed, and one that is new fires once spend
// has reached it.
type AlertClaim struct {
	Scope            Scope
	Subject          string
	EffectiveSubject string
	Revision         string
	PeriodSeconds    int64
	PeriodStart      time.Time
	PeriodEnd        time.Time
	Threshold        int
}

// key identifies the claim within one process. The event ID is derived from
// it, so it must only ever change together with the claim identity.
func (c AlertClaim) key() string {
	return strings.Join([]string{
		string(c.Scope), c.Subject, c.EffectiveSubject, c.Revision,
		strconv.FormatInt(c.PeriodSeconds, 10),
		strconv.FormatInt(c.PeriodStart.Unix(), 10),
		strconv.Itoa(c.Threshold),
	}, "\x00")
}

// alertRevision identifies the version of a budget its alerts were fired
// for: its creation time, in seconds as every store keeps it, and its amount.
// Every replica reads both from the database, so they agree on it.
func (b Budget) alertRevision() string {
	var created int64
	if !b.CreatedAt.IsZero() {
		created = b.CreatedAt.Unix()
	}
	return strconv.FormatInt(created, 10) + ":" + strconv.FormatFloat(b.Amount, 'f', -1, 64)
}

// ChildSpend is the spend of one direct child of a per-child template: the
// child's own usage plus that of every path below it.
type ChildSpend struct {
	Child string
	Total float64
}

// AlertStore is the storage alert evaluation needs beyond Store. The SQL and
// MongoDB stores implement it.
type AlertStore interface {
	// ClaimAlert records that the alert fired and reports whether this call
	// was the first to do so. Every gateway instance on the database claims
	// before it delivers, so an alert is delivered once.
	ClaimAlert(ctx context.Context, claim AlertClaim) (bool, error)

	// PruneAlerts forgets the alerts of periods that ended before cutoff.
	PruneAlerts(ctx context.Context, cutoff time.Time) error

	// SumSpendByChild totals the spend of a user-path window per direct child
	// of the window's subject, each child including every path below it, as
	// enforcement counts a per-child budget. It returns the largest at most
	// limit children; usage recorded on the subject itself is nobody's child
	// and left out.
	SumSpendByChild(ctx context.Context, window SpendWindow, limit int) ([]ChildSpend, error)
}

// encodeAlertThresholds renders thresholds for the SQL budgets table.
func encodeAlertThresholds(thresholds []int) string {
	parts := make([]string, len(thresholds))
	for i, threshold := range thresholds {
		parts[i] = strconv.Itoa(threshold)
	}
	return strings.Join(parts, ",")
}

// decodeAlertThresholds reads thresholds written by encodeAlertThresholds,
// skipping anything that is not a number.
func decodeAlertThresholds(raw string) []int {
	var thresholds []int
	for part := range strings.SplitSeq(raw, ",") {
		if value, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			thresholds = append(thresholds, value)
		}
	}
	return thresholds
}
