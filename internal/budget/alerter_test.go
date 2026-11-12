package budget

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/enterpilot/gomodel/internal/usage"
)

// alertRecorder is the alert destination of a test instance.
type alertRecorder struct {
	mu     sync.Mutex
	events []ext.AlertEvent
}

func (r *alertRecorder) notify(event ext.AlertEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// take returns the alerts recorded since the last call.
func (r *alertRecorder) take() []ext.AlertEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events
	r.events = nil
	return events
}

// alertInstance is one gateway instance: its own store handle, service and
// alerter on a shared database, with a clock the test moves.
type alertInstance struct {
	service  *Service
	alerter  *Alerter
	recorder *alertRecorder
	now      time.Time
}

func newAlertInstance(t *testing.T, db sqlx.DB, options ...ServiceOption) *alertInstance {
	t.Helper()
	ctx := context.Background()
	store, err := NewSQLStore(ctx, db)
	require.NoError(t, err)
	return newAlertInstanceOn(t, store, options...)
}

// newAlertInstanceOn is newAlertInstance for a store already opened.
func newAlertInstanceOn(t *testing.T, store Store, options ...ServiceOption) *alertInstance {
	t.Helper()
	service, err := NewService(context.Background(), store, options...)
	require.NoError(t, err)

	instance := &alertInstance{service: service, recorder: &alertRecorder{}, now: alertTestNow}
	instance.alerter, err = NewAlerter(service, time.Minute, instance.recorder.notify)
	require.NoError(t, err)
	require.NotNil(t, instance.alerter)
	instance.alerter.now = func() time.Time { return instance.now }
	return instance
}

// evaluate runs one pass and returns what it fired, as "subject@threshold".
func (i *alertInstance) evaluate(t *testing.T) []string {
	t.Helper()
	require.NoError(t, i.alerter.Evaluate(context.Background()))
	return alertNames(i.recorder.take())
}

func alertNames(events []ext.AlertEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Budget.EffectiveSubject+"@"+strconv.Itoa(event.ThresholdPercent))
	}
	return names
}

// spend records priced usage for userPath at ts.
func spend(t *testing.T, usageStore *usage.SQLStore, name, userPath string, ts time.Time, cost float64, labels ...string) {
	t.Helper()
	err := usageStore.WriteBatch(context.Background(), []*usage.UsageEntry{usageEntryWithCost(name, userPath, "", ts, cost, labels...)})
	require.NoError(t, err)
}

func TestAlerterFiresEachThresholdOnceAcrossInstances(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)

		// Two gateway instances on one database.
		first, second := newAlertInstance(t, db), newAlertInstance(t, db)
		require.NoError(t, first.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 100, Source: SourceManual},
		}))
		require.NoError(t, second.service.Refresh(ctx))

		assert.Empty(t, first.evaluate(t), "no spend, no alert")

		spend(t, usageStore, "below", "/team/app", alertTestNow.Add(-2*time.Hour), 79)
		assert.Empty(t, first.evaluate(t), "79% is below every threshold")

		spend(t, usageStore, "crossing-80", "/team/app", alertTestNow.Add(-time.Hour), 6)
		require.NoError(t, first.alerter.Evaluate(ctx))
		events := first.recorder.take()
		require.Len(t, events, 1)

		projected := 170.0 // 85 spent halfway through the day
		exhaustion := time.Date(2026, time.April, 25, 14, 7, 3, 0, time.UTC)
		budgets := first.service.Budgets()
		require.Len(t, budgets, 1)
		assert.Equal(t, ext.AlertEvent{
			SchemaVersion: ext.AlertSchemaVersion,
			Type:          ext.AlertTypeBudgetThreshold,
			ID:            events[0].ID,
			Timestamp:     alertTestNow,
			Budget: ext.AlertBudget{
				Scope: "user_path", Subject: "/team", EffectiveSubject: "/team",
				PeriodSeconds: PeriodDailySeconds, Period: "daily", Amount: 100, CostBasis: "provider",
				Revision: strconv.FormatInt(budgets[0].CreatedAt.Unix(), 10) + ":100",
			},
			ThresholdPercent: 80,
			Spent:            85,
			UsageRatio:       0.85,
			PeriodStart:      time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC),
			PeriodEnd:        time.Date(2026, time.April, 26, 0, 0, 0, 0, time.UTC),
			ProjectedSpend:   &projected,

			ProjectedExhaustionAt: &exhaustion,
		}, events[0])
		assert.Regexp(t, `^bgt_[0-9a-f]{24}$`, events[0].ID)

		assert.Empty(t, second.evaluate(t), "the other instance must not fire the claimed alert")
		assert.Empty(t, first.evaluate(t), "nor the first one again")

		// Both instances notice the next threshold at the same moment.
		spend(t, usageStore, "crossing-100", "/team", alertTestNow.Add(-time.Minute), 20)
		var wg sync.WaitGroup
		for _, instance := range []*alertInstance{first, second} {
			wg.Go(func() { assert.NoError(t, instance.alerter.Evaluate(ctx)) })
		}
		wg.Wait()
		fired := append(alertNames(first.recorder.take()), alertNames(second.recorder.take())...)
		assert.Equal(t, []string{"/team@100"}, fired, "exactly one instance fires it")
	})
}

func TestAlerterFiresAgainAfterResetAndInANewPeriod(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		instance := newAlertInstance(t, db)
		require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeLabel, Subject: "prod", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		}))

		spend(t, usageStore, "before-reset", "/a", alertTestNow.Add(-time.Hour), 9, "prod")
		assert.Equal(t, []string{"prod@80"}, instance.evaluate(t))

		// A reset starts a new period at the reset: spend before it is gone...
		require.NoError(t, instance.service.ResetBudget(ctx, ScopeLabel, "prod", PeriodDailySeconds, alertTestNow))
		instance.now = alertTestNow.Add(time.Hour)
		assert.Empty(t, instance.evaluate(t))

		// ...and reaching the threshold again fires again.
		spend(t, usageStore, "after-reset", "/a", alertTestNow.Add(30*time.Minute), 8.5, "prod")
		require.NoError(t, instance.alerter.Evaluate(ctx))
		events := instance.recorder.take()
		require.Len(t, events, 1)
		assert.Equal(t, 80, events[0].ThresholdPercent)
		assert.Equal(t, alertTestNow, events[0].PeriodStart, "the period of the alert starts at the reset")
		afterReset := events[0].ID

		// The next calendar period fires on its own spend too.
		instance.now = alertTestNow.AddDate(0, 0, 1)
		assert.Empty(t, instance.evaluate(t))
		spend(t, usageStore, "next-day", "/a", instance.now.Add(-time.Hour), 10, "prod")
		require.NoError(t, instance.alerter.Evaluate(ctx))
		events = instance.recorder.take()
		assert.Equal(t, []string{"prod@80", "prod@100"}, alertNames(events), "every threshold reached fires, lowest first")
		assert.Equal(t, time.Date(2026, time.April, 26, 0, 0, 0, 0, time.UTC), events[0].PeriodStart)
		assert.NotEqual(t, afterReset, events[0].ID, "an alert of another period has another ID")
	})
}

func TestAlerterEvaluatesPerChildBudgets(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		usageStore, err := usage.NewSQLStore(ctx, db, 0)
		require.NoError(t, err)
		first, second := newAlertInstance(t, db), newAlertInstance(t, db)
		require.NoError(t, first.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
			// A plain budget on one child is a different budget with its own alerts.
			{Scope: ScopeUserPath, Subject: "/customers/alice", PeriodSeconds: PeriodDailySeconds, Amount: 9, AlertThresholds: []int{100}, Source: SourceManual},
		}))
		require.NoError(t, second.service.Refresh(ctx))

		at := alertTestNow.Add(-time.Hour)
		spend(t, usageStore, "alice", "/customers/alice", at, 5)
		spend(t, usageStore, "alice-app", "/customers/alice/app", at, 4) // deeper paths count for the child
		spend(t, usageStore, "bob", "/customers/bob", at, 3)
		spend(t, usageStore, "carol", "/customers/carol", at, 10.5)
		spend(t, usageStore, "template", "/customers", at, 100) // the template path is nobody's child

		require.NoError(t, first.alerter.Evaluate(ctx))
		events := first.recorder.take()
		assert.ElementsMatch(t, []string{
			"/customers/alice@100", // the plain budget: 9 of 9
			"/customers/alice@80",  // the template: 9 of 10
			"/customers/carol@80", "/customers/carol@100",
		}, alertNames(events))

		for _, event := range events {
			if event.Budget.Subject != "/customers" {
				continue
			}
			assert.True(t, event.Budget.PerChild)
			assert.Equal(t, float64(10), event.Budget.Amount)
			if event.Budget.EffectiveSubject == "/customers/alice" {
				assert.Equal(t, float64(9), event.Spent)
				assert.Equal(t, 0.9, event.UsageRatio)
			}
		}

		assert.Empty(t, second.evaluate(t), "children are deduplicated across instances too")

		spend(t, usageStore, "bob-more", "/customers/bob", at, 5)
		assert.Equal(t, []string{"/customers/bob@80"}, second.evaluate(t))

		// Resetting the template starts a new period for every child.
		require.NoError(t, first.service.ResetBudget(ctx, ScopeUserPath, "/customers", PeriodDailySeconds, alertTestNow))
		first.now = alertTestNow.Add(time.Hour)
		spend(t, usageStore, "carol-after-reset", "/customers/carol", alertTestNow.Add(time.Minute), 8)
		assert.Equal(t, []string{"/customers/carol@80"}, first.evaluate(t))
	})
}

// TestAlerterDeduplicatesAcrossInstancesOnMongoDB runs the replica contract
// end to end on MongoDB: two instances, plain and per-child budgets, a reset.
func TestAlerterDeduplicatesAcrossInstancesOnMongoDB(t *testing.T) {
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		ctx := context.Background()
		instance := func() *alertInstance {
			store, err := NewMongoDBStore(ctx, db)
			require.NoError(t, err)
			return newAlertInstanceOn(t, store)
		}
		first, second := instance(), instance()
		require.NoError(t, first.service.UpsertBudgets(ctx, []Budget{
			{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 100, Source: SourceManual},
			{Scope: ScopeUserPath, Subject: "/customers", PerChild: true, PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		}))
		require.NoError(t, second.service.Refresh(ctx))

		usageRow := func(userPath string, ts time.Time, cost float64) bson.D {
			return bson.D{{Key: "timestamp", Value: ts}, {Key: "user_path", Value: userPath}, {Key: "total_cost", Value: cost}}
		}
		at := alertTestNow.Add(-time.Hour)
		_, err := db.Collection("usage").InsertMany(ctx, []any{
			usageRow("/team/app", at, 85),
			usageRow("/customers/alice", at, 9),
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		for _, instance := range []*alertInstance{first, second} {
			wg.Go(func() { assert.NoError(t, instance.alerter.Evaluate(ctx)) })
		}
		wg.Wait()
		fired := append(alertNames(first.recorder.take()), alertNames(second.recorder.take())...)
		assert.ElementsMatch(t, []string{"/team@80", "/customers/alice@80"}, fired, "each alert fires on exactly one instance")
		assert.Empty(t, first.evaluate(t))
		assert.Empty(t, second.evaluate(t))

		require.NoError(t, first.service.ResetBudget(ctx, ScopeUserPath, "/customers", PeriodDailySeconds, alertTestNow))
		require.NoError(t, second.service.Refresh(ctx))
		second.now = alertTestNow.Add(time.Hour)
		_, err = db.Collection("usage").InsertOne(ctx, usageRow("/customers/alice", alertTestNow.Add(time.Minute), 8))
		require.NoError(t, err)
		assert.Equal(t, []string{"/customers/alice@80"}, second.evaluate(t), "a reset fires again")
	})
}

func TestAlerterUsesPerBudgetThresholds(t *testing.T) {
	db := sqlxtest.NewSQLite(t)
	ctx := context.Background()
	usageStore, err := usage.NewSQLStore(ctx, db, 0)
	require.NoError(t, err)
	instance := newAlertInstance(t, db, WithAlertThresholds([]int{90}))
	require.NoError(t, instance.service.UpsertBudgets(ctx, []Budget{
		{Scope: ScopeUserPath, Subject: "/default", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
		{Scope: ScopeUserPath, Subject: "/own", PeriodSeconds: PeriodDailySeconds, Amount: 10, AlertThresholds: []int{25, 50}, Source: SourceManual},
		{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMonthlySeconds, Amount: 1000, Source: SourceManual},
	}))

	at := alertTestNow.Add(-time.Hour)
	spend(t, usageStore, "default", "/default", at, 6)
	spend(t, usageStore, "own", "/own", at, 6)

	assert.Equal(t, []string{"/own@25", "/own@50"}, instance.evaluate(t), "60% is below the default of 90")

	spend(t, usageStore, "default-more", "/default", at, 3)
	assert.Equal(t, []string{"/default@90"}, instance.evaluate(t))
}

// countingAlertStore counts the storage calls of an alerter.
type countingAlertStore struct {
	*SQLStore
	mu     sync.Mutex
	claims int
	prunes int
}

func (s *countingAlertStore) ClaimAlert(ctx context.Context, claim AlertClaim) (bool, error) {
	s.mu.Lock()
	s.claims++
	s.mu.Unlock()
	return s.SQLStore.ClaimAlert(ctx, claim)
}

func (s *countingAlertStore) PruneAlerts(ctx context.Context, cutoff time.Time) error {
	s.mu.Lock()
	s.prunes++
	s.mu.Unlock()
	return s.SQLStore.PruneAlerts(ctx, cutoff)
}

// A budget that stays over a threshold must not cost a write every pass.
func TestAlerterKeepsStorageWorkBounded(t *testing.T) {
	db := sqlxtest.NewSQLite(t)
	ctx := context.Background()
	usageStore, err := usage.NewSQLStore(ctx, db, 0)
	require.NoError(t, err)
	sqlStore, err := NewSQLStore(ctx, db)
	require.NoError(t, err)
	store := &countingAlertStore{SQLStore: sqlStore}
	service, err := NewService(ctx, store)
	require.NoError(t, err)
	require.NoError(t, service.UpsertBudgets(ctx, []Budget{
		{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10, Source: SourceManual},
	}))
	recorder := &alertRecorder{}
	alerter, err := NewAlerter(service, time.Minute, recorder.notify)
	require.NoError(t, err)
	now := alertTestNow
	alerter.now = func() time.Time { return now }

	spend(t, usageStore, "over", "/team", now.Add(-time.Hour), 12)
	for range 5 {
		require.NoError(t, alerter.Evaluate(ctx))
		now = now.Add(time.Minute)
	}
	assert.Len(t, recorder.take(), 2)
	assert.Equal(t, 2, store.claims, "one claim per threshold, not one per pass")
	assert.Equal(t, 1, store.prunes, "pruning runs once an interval")
	assert.Len(t, alerter.settled, 2)

	// Once the period is over its claims are forgotten and pruning runs again.
	now = now.Add(48 * time.Hour)
	require.NoError(t, alerter.Evaluate(ctx))
	assert.Empty(t, alerter.settled)
	assert.Equal(t, 2, store.prunes)

	won, err := sqlStore.ClaimAlert(ctx, AlertClaim{
		Scope: ScopeUserPath, Subject: "/team", EffectiveSubject: "/team", PeriodSeconds: PeriodDailySeconds,
		PeriodStart: time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC), Threshold: 80,
	})
	require.NoError(t, err)
	assert.True(t, won, "the claims of the ended period were pruned from storage")
}

func TestNewAlerterNeedsADestination(t *testing.T) {
	db := sqlxtest.NewSQLite(t)
	store, err := NewSQLStore(context.Background(), db)
	require.NoError(t, err)
	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	alerter, err := NewAlerter(service, time.Minute, nil)
	require.NoError(t, err)
	require.Nil(t, alerter, "without a destination nothing is evaluated")
	assert.False(t, service.AlertSettings().Enabled)
	stop := alerter.Start()
	stop()

	alerter, err = NewAlerter(service, 0, func(ext.AlertEvent) {})
	require.NoError(t, err)
	require.NotNil(t, alerter)
	assert.True(t, service.AlertSettings().Enabled)
	assert.Equal(t, time.Minute, alerter.interval)

	// A store without alert support is a startup error, not a silent no-op.
	plain, err := NewService(context.Background(), &fakeStore{})
	require.NoError(t, err)
	_, err = NewAlerter(plain, time.Minute, func(ext.AlertEvent) {})
	require.ErrorContains(t, err, "does not support alerts")
}

// memoryAlertStore is fakeStore with in-memory alert storage, for tests that
// run on a fake clock.
type memoryAlertStore struct {
	fakeStore
	mu       sync.Mutex
	claimed  map[string]bool
	claimErr error
}

func (s *memoryAlertStore) ClaimAlert(_ context.Context, claim AlertClaim) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.claimed[claim.key()] {
		return false, nil
	}
	if s.claimed == nil {
		s.claimed = make(map[string]bool)
	}
	s.claimed[claim.key()] = true
	return true, nil
}

func (s *memoryAlertStore) PruneAlerts(context.Context, time.Time) error { return nil }

func (s *memoryAlertStore) SumSpendByChild(context.Context, SpendWindow, int) ([]ChildSpend, error) {
	return nil, nil
}

func TestAlerterStartEvaluatesUntilStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryAlertStore{}
		store.budgets = []Budget{{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourlySeconds, Amount: 10}}
		var passes atomic.Int32
		store.sum = func(SpendWindow) (float64, bool, error) {
			passes.Add(1)
			return 9, true, nil
		}
		service, err := NewService(context.Background(), store)
		require.NoError(t, err)
		recorder := &alertRecorder{}
		alerter, err := NewAlerter(service, time.Minute, recorder.notify)
		require.NoError(t, err)

		stop := alerter.Start()
		synctest.Wait()
		assert.Empty(t, recorder.take(), "nothing runs before the first interval")
		assert.Zero(t, passes.Load())

		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		assert.Equal(t, []string{"/team@80"}, alertNames(recorder.take()))
		assert.Equal(t, int32(1), passes.Load())

		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Empty(t, recorder.take(), "the alert already fired")
		assert.Equal(t, int32(2), passes.Load())

		stop()
		stop() // idempotent
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		assert.Equal(t, int32(2), passes.Load(), "a stopped alerter evaluates nothing")
	})
}

// A claim that fails in storage is retried on the next pass instead of being
// remembered as settled.
func TestAlerterRetriesAFailedClaim(t *testing.T) {
	store := &memoryAlertStore{claimErr: errors.New("database is down")}
	store.budgets = []Budget{{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10}}
	store.sum = func(SpendWindow) (float64, bool, error) { return 8, true, nil }
	service, err := NewService(context.Background(), store)
	require.NoError(t, err)
	recorder := &alertRecorder{}
	alerter, err := NewAlerter(service, time.Minute, recorder.notify)
	require.NoError(t, err)
	alerter.now = func() time.Time { return alertTestNow }

	require.NoError(t, alerter.Evaluate(context.Background()))
	assert.Empty(t, recorder.take())

	store.mu.Lock()
	store.claimErr = nil
	store.mu.Unlock()
	require.NoError(t, alerter.Evaluate(context.Background()))
	assert.Equal(t, []string{"/team@80"}, alertNames(recorder.take()))
}
