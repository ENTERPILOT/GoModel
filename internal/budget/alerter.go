package budget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/ext"
)

const (
	// alertEvaluationTimeout bounds the storage work of one evaluation.
	alertEvaluationTimeout = 30 * time.Second
	// alertChildLimit caps the children read for one per-child template in
	// one evaluation. Children are ranked by their whole spend, paths below
	// them included, so the ones left out are the smallest spenders.
	alertChildLimit = 10000
	// alertPruneInterval is how often fired alerts of ended periods are
	// deleted; alertPruneGrace keeps them a while past the period end.
	alertPruneInterval = time.Hour
	alertPruneGrace    = 24 * time.Hour
)

// Alerter evaluates every budget against its alert thresholds in the
// background and hands each alert that fires to notify. It never runs on the
// request path.
//
// An alert fires once per budget revision, period, and threshold across every
// gateway instance sharing the database: an instance claims the alert in
// storage before notifying, and only the first claim wins. A reset, a new
// period, a moved period anchor, a changed amount, or a recreated budget is a
// new claim and so fires again; see AlertClaim. Claiming first means delivery
// is at most once: an instance that stops between the claim and the delivery
// loses that alert.
type Alerter struct {
	service  *Service
	store    AlertStore
	notify   func(ext.AlertEvent)
	interval time.Duration
	now      func() time.Time
	// childLimit is alertChildLimit; tests lower it.
	childLimit int

	mu sync.Mutex
	// settled remembers the claims this instance already made or lost, by
	// claim key, with the period end after which they are forgotten. It keeps
	// a budget that stays over a threshold from costing a write every pass.
	settled   map[string]time.Time
	lastPrune time.Time
}

// NewAlerter returns the alert evaluator of service, or nil when notify is
// nil: without a destination nothing is evaluated.
func NewAlerter(service *Service, interval time.Duration, notify func(ext.AlertEvent)) (*Alerter, error) {
	if notify == nil || service == nil {
		return nil, nil
	}
	store, ok := service.store.(AlertStore)
	if !ok {
		return nil, fmt.Errorf("budget store %T does not support alerts", service.store)
	}
	if interval <= 0 {
		interval = time.Minute
	}
	service.alertsEnabled.Store(true)
	return &Alerter{
		service:  service,
		store:    store,
		notify:   notify,
		interval: interval,
		now:      time.Now,
		settled:  make(map[string]time.Time),

		childLimit: alertChildLimit,
	}, nil
}

// Start evaluates periodically until the returned function is called. The
// function waits for a running evaluation and is safe to call repeatedly. A
// nil Alerter starts nothing.
func (a *Alerter) Start() func() {
	if a == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				evalCtx, evalCancel := context.WithTimeout(ctx, alertEvaluationTimeout)
				if err := a.Evaluate(evalCtx); err != nil && ctx.Err() == nil {
					slog.Warn("budget alert evaluation failed", "error", err)
				}
				evalCancel()
			}
		}
	}()
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// Evaluate runs one pass: it reads the spend of every budget, in one batched
// lookup plus one grouped lookup per per-child template, and fires the
// thresholds reached and not yet claimed.
func (a *Alerter) Evaluate(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	budgets, settings, now, err := a.service.match(nil, a.now())
	if err != nil {
		return err
	}
	a.forgetEnded(now)

	var plain, templates []Budget
	for _, budget := range budgets {
		if budget.PerChild {
			templates = append(templates, budget)
		} else {
			plain = append(plain, budget)
		}
	}
	// One failed lookup must not silence the budgets that were read.
	var errs []error
	results, err := a.service.evaluate(ctx, plain, now, settings)
	if err != nil {
		errs = append(errs, err)
	}
	for _, template := range templates {
		children, err := a.evaluateChildren(ctx, template, now, settings)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, children...)
	}
	for _, result := range results {
		a.fire(ctx, result, now)
	}
	errs = append(errs, a.prune(ctx, now))
	return errors.Join(errs...)
}

// evaluateChildren resolves a per-child template to one result per direct
// child that has spend in the current period. The store totals each child
// over its whole subtree, so a child's spend is what enforcement counts.
func (a *Alerter) evaluateChildren(ctx context.Context, template Budget, now time.Time, settings Settings) ([]CheckResult, error) {
	start, end := activePeriod(template, now, settings)
	children, err := a.store.SumSpendByChild(ctx, SpendWindow{
		Scope:     ScopeUserPath,
		Subject:   template.Subject,
		CostBasis: template.EnforcedBasis(),
		Start:     start,
		End:       now,
	}, a.childLimit)
	if err != nil {
		return nil, err
	}
	if len(children) == a.childLimit {
		slog.Warn("per-child budget has more children with spend than one alert evaluation reads; the children that spent least are not evaluated",
			"subject", template.Subject, "period", PeriodLabel(template.PeriodSeconds), "limit", a.childLimit)
	}

	results := make([]CheckResult, 0, len(children))
	for _, child := range children {
		budget := template
		budget.EffectiveSubject = child.Child
		results = append(results, CheckResult{
			Budget:      budget,
			PeriodStart: start,
			PeriodEnd:   end,
			Spent:       child.Total,
			HasUsage:    true,
			Remaining:   budget.Amount - child.Total,
		})
	}
	return results, nil
}

// fire claims and notifies every threshold result has reached. A claim that
// fails in storage is logged and tried again on the next pass.
func (a *Alerter) fire(ctx context.Context, result CheckResult, now time.Time) {
	for _, threshold := range a.service.alertThresholds(result.Budget) {
		if !result.thresholdReached(threshold) {
			return // thresholds ascend
		}
		claim := AlertClaim{
			Scope:            result.Budget.Scope,
			Subject:          result.Budget.Subject,
			EffectiveSubject: result.Budget.evaluationSubject(),
			Revision:         result.Budget.alertRevision(),
			PeriodSeconds:    result.Budget.PeriodSeconds,
			PeriodStart:      result.PeriodStart,
			PeriodEnd:        result.PeriodEnd,
			Threshold:        threshold,
		}
		key := claim.key()
		if _, done := a.settled[key]; done {
			continue
		}
		won, err := a.store.ClaimAlert(ctx, claim)
		if err != nil {
			if ctx.Err() != nil {
				return // stopping; the next pass claims it
			}
			slog.Warn("budget alert claim failed", "error", err,
				"scope", claim.Scope, "subject", claim.EffectiveSubject, "threshold", threshold)
			continue
		}
		a.settled[key] = claim.PeriodEnd
		if won {
			a.notify(alertEvent(claim, result, now))
		}
	}
}

// forgetEnded drops the remembered claims of periods that are over.
func (a *Alerter) forgetEnded(now time.Time) {
	for key, periodEnd := range a.settled {
		if periodEnd.Before(now) {
			delete(a.settled, key)
		}
	}
}

// prune deletes stored claims of periods long over, at most once per
// alertPruneInterval.
func (a *Alerter) prune(ctx context.Context, now time.Time) error {
	if !a.lastPrune.IsZero() && now.Sub(a.lastPrune) < alertPruneInterval {
		return nil
	}
	if err := a.store.PruneAlerts(ctx, now.Add(-alertPruneGrace)); err != nil {
		return err
	}
	a.lastPrune = now
	return nil
}

// alertEvent builds the public event of one fired alert. The ID is derived
// from the claim, so it is the same wherever and however often it is built.
func alertEvent(claim AlertClaim, result CheckResult, now time.Time) ext.AlertEvent {
	sum := sha256.Sum256([]byte(claim.key()))
	event := ext.AlertEvent{
		SchemaVersion: ext.AlertSchemaVersion,
		Type:          ext.AlertTypeBudgetThreshold,
		ID:            "bgt_" + hex.EncodeToString(sum[:12]),
		Timestamp:     now.UTC(),
		Budget: ext.AlertBudget{
			Scope:            string(claim.Scope),
			Subject:          claim.Subject,
			EffectiveSubject: claim.EffectiveSubject,
			PerChild:         result.Budget.PerChild,
			PeriodSeconds:    claim.PeriodSeconds,
			Period:           PeriodLabel(claim.PeriodSeconds),
			Amount:           result.Budget.Amount,
			CostBasis:        string(result.Budget.EnforcedBasis()),
			Revision:         claim.Revision,
		},
		ThresholdPercent: claim.Threshold,
		Spent:            result.Spent,
		UsageRatio:       result.UsageRatio(),
		PeriodStart:      result.PeriodStart.UTC(),
		PeriodEnd:        result.PeriodEnd.UTC(),
	}
	if projection, ok := result.Projection(now); ok {
		event.ProjectedSpend = &projection.Spend
		event.ProjectedExhaustionAt = projection.ExhaustionAt
	}
	return event
}
